package uplevel

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/linuxsuren/local-gb28181-adapter/internal/gbsdp"
	"github.com/linuxsuren/local-gb28181-adapter/internal/psrtp"
)

// SessionInfo 点播会话信息（管理 API 展示用）。
type SessionInfo struct {
	ChannelID string    `json:"channel_id"`
	DeviceID  string    `json:"device_id"`
	CallID    string    `json:"call_id"`
	Remote    string    `json:"remote"`     // 设备信令地址
	MediaPort int       `json:"media_port"` // 本机收流 UDP 端口
	SSRC      uint32    `json:"ssrc"`
	StartedAt time.Time `json:"started_at"`
	Frames    int64     `json:"frames"`
	Bytes     int64     `json:"bytes"`
	LastFrame time.Time `json:"last_frame"`
}

// Session 一次点播会话：INVITE 对话 + 收流器 + 帧出口。
type Session struct {
	channelID string
	deviceID  string
	callID    string
	remote    string
	mediaPort int
	ssrc      uint32
	startedAt time.Time

	receiver *psrtp.UDPReceiver
	writer   FrameWriter
	dlg      *sipgo.DialogClientSession

	frames    atomic.Int64
	bytes     atomic.Int64
	lastFrame atomic.Value // time.Time
	done      chan struct{}
}

func (sess *Session) info() SessionInfo {
	last, _ := sess.lastFrame.Load().(time.Time)
	return SessionInfo{
		ChannelID: sess.channelID,
		DeviceID:  sess.deviceID,
		CallID:    sess.callID,
		Remote:    sess.remote,
		MediaPort: sess.mediaPort,
		SSRC:      sess.ssrc,
		StartedAt: sess.startedAt,
		Frames:    sess.frames.Load(),
		Bytes:     sess.bytes.Load(),
		LastFrame: last,
	}
}

// Sessions 返回全部点播会话快照。
func (s *Service) Sessions() []SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionInfo, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess.info())
	}
	return out
}

// Play 对通道发起 INVITE 点播；已在播时直接返回现有会话。
// 收流地址为本机（AdvertiseIP:UDP 端口），媒体 UDP 模式。
func (s *Service) Play(ctx context.Context, channelID string) (*SessionInfo, error) {
	s.mu.Lock()
	if sess, ok := s.sessions[channelID]; ok {
		info := sess.info()
		s.mu.Unlock()
		return &info, nil
	}
	s.mu.Unlock()

	dev, _, ok := s.Registry.FindByChannel(channelID)
	if !ok {
		return nil, fmt.Errorf("channel %s not found in any device catalog (query catalog first)", channelID)
	}

	up := s.store.Root().Uplevel

	// 分配收流端口（配置范围内找空闲）。
	receiver, port, err := s.newReceiver(up.MediaPortMin, up.MediaPortMax)
	if err != nil {
		return nil, fmt.Errorf("alloc media port: %w", err)
	}

	var writer FrameWriter
	if s.sink != nil {
		writer, err = s.sink.OpenChannel(channelID)
		if err != nil {
			_ = receiver.Close()
			return nil, fmt.Errorf("open stream sink: %w", err)
		}
	} else {
		writer = noopWriter{}
	}

	sess := &Session{
		channelID: channelID,
		deviceID:  dev.DeviceID,
		remote:    net.JoinHostPort(dev.RemoteIP, strconv.Itoa(dev.RemotePort)),
		mediaPort: port,
		startedAt: time.Now(),
		receiver:  receiver,
		writer:    writer,
		done:      make(chan struct{}),
	}
	sess.lastFrame.Store(time.Time{})

	// 收流先就绪再发 INVITE。
	receiver.Start(func(frame []byte, pts uint64) {
		sess.frames.Add(1)
		sess.bytes.Add(int64(len(frame)))
		sess.lastFrame.Store(time.Now())
		if err := writer.WriteFrame(frame, pts); err != nil {
			// 丢包残帧较常见，降为 Debug 避免刷屏（frames 计数仍可见）。
			s.logger.Debug("uplevel frame write failed", "channel", channelID, "err", err.Error())
		}
	})

	ssrc := liveSSRC(up.ServerID)
	offer := gbsdp.BuildPlatformOffer(channelID, s.AdvertiseIP, port, ssrc)

	s.mu.Lock()
	dialogUA := s.dialog
	s.mu.Unlock()
	if dialogUA == nil {
		_ = receiver.Close()
		writer.Close()
		return nil, fmt.Errorf("uplevel not running")
	}

	recipient := sip.Uri{Scheme: "sip", User: channelID, Host: dev.RemoteIP, Port: dev.RemotePort}
	fromTag, terr := randomTag()
	if terr != nil {
		_ = receiver.Close()
		writer.Close()
		return nil, fmt.Errorf("gen from tag: %w", terr)
	}
	dlg, err := dialogUA.Invite(ctx, recipient, []byte(offer),
		sip.NewHeader("From", fmt.Sprintf("<sip:%s@%s>;tag=%s", up.ServerID, up.Realm(), fromTag)),
		sip.NewHeader("To", fmt.Sprintf("<sip:%s@%s>", channelID, up.Realm())),
		sip.NewHeader("Content-Type", "application/sdp"),
		sip.NewHeader("Subject", fmt.Sprintf("%s:%s,%s:0", channelID,
			strconv.FormatUint(uint64(ssrc), 10), up.ServerID)),
	)
	if err != nil {
		_ = receiver.Close()
		writer.Close()
		return nil, fmt.Errorf("send invite: %w", err)
	}
	if err := dlg.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		_ = dlg.Close()
		_ = receiver.Close()
		writer.Close()
		return nil, fmt.Errorf("wait invite answer: %w", err)
	}
	if err := dlg.Ack(ctx); err != nil {
		_ = dlg.Close()
		_ = receiver.Close()
		writer.Close()
		return nil, fmt.Errorf("send ack: %w", err)
	}

	// 设备应答的 SSRC 可能与 offer 不同，以应答为准（仅展示）。
	if answer, perr := gbsdp.ParseOffer(string(dlg.InviteResponse.Body())); perr == nil && answer.SSRC != 0 {
		ssrc = answer.SSRC
	}

	sess.dlg = dlg
	sess.ssrc = ssrc
	sess.callID = dlg.ID

	s.mu.Lock()
	// 竞态兜底：并发 Play 同一通道，保留先登记者。
	if exist, ok := s.sessions[channelID]; ok {
		info := exist.info()
		s.mu.Unlock()
		sess.close("replaced by existing session")
		return &info, nil
	}
	s.sessions[channelID] = sess
	s.mu.Unlock()

	// 会话空闲看护：5 分钟无帧自动回收（对端 BYE 丢失场景）。
	go sess.watchdog()

	info := sess.info()
	s.logger.Info("uplevel invite answered",
		"channel", channelID,
		"device", dev.DeviceID,
		"call", sess.callID,
		"media", net.JoinHostPort(s.AdvertiseIP, strconv.Itoa(port)),
		"ssrc", ssrc)
	return &info, nil
}

// StopPlay 停止通道点播（发 BYE 并回收资源）。
func (s *Service) StopPlay(channelID string) error {
	s.mu.Lock()
	sess, ok := s.sessions[channelID]
	if ok {
		delete(s.sessions, channelID)
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("no play session for channel %s", channelID)
	}
	sess.close("stop")
	return nil
}

// onBye 处理设备侧 BYE（对端挂断）。
func (s *Service) onBye(req *sip.Request, tx sip.ServerTransaction) {
	callID := ""
	if c := req.CallID(); c != nil {
		callID = c.Value()
	}
	var victim *Session
	s.mu.Lock()
	for id, sess := range s.sessions {
		if sess.callID == callID {
			victim = sess
			delete(s.sessions, id)
			break
		}
	}
	s.mu.Unlock()
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	if victim != nil {
		victim.close("bye from device")
		s.logger.Info("uplevel bye", "channel", victim.channelID, "call", callID)
	}
}

// close 回收会话资源（BYE 由调用方决定是否已发送）。
func (sess *Session) close(reason string) {
	select {
	case <-sess.done:
		return // 已回收
	default:
		close(sess.done)
	}
	if sess.dlg != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = sess.dlg.Bye(ctx)
		cancel()
		_ = sess.dlg.Close()
	}
	if sess.receiver != nil {
		_ = sess.receiver.Close()
	}
	if sess.writer != nil {
		sess.writer.Close()
	}
}

// watchdog 空闲回收：ACK 后一直无帧，或会话结束后泄漏保护。
func (sess *Session) watchdog() {
	const idleTimeout = 5 * time.Minute
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-sess.done:
			return
		case <-t.C:
			last, _ := sess.lastFrame.Load().(time.Time)
			if !last.IsZero() && time.Since(last) > idleTimeout {
				sess.close("idle timeout")
				return
			}
		}
	}
}

// newReceiver 在端口范围内创建 UDP 收流器。
func (s *Service) newReceiver(minPort, maxPort int) (*psrtp.UDPReceiver, int, error) {
	if maxPort <= minPort {
		minPort, maxPort = 30000, 30499
	}
	for p := minPort; p <= maxPort; p++ {
		r, err := psrtp.NewUDPReceiver(p)
		if err == nil {
			return r, p, nil
		}
	}
	return nil, 0, fmt.Errorf("no free udp port in [%d,%d]", minPort, maxPort)
}

// liveSSRC 生成实时点播 SSRC（GB28181 惯例：0 + 9 位随机数）。
func liveSSRC(serverID string) uint32 {
	n, err := rand.Int(rand.Reader, big.NewInt(899999999))
	if err != nil {
		var b [4]byte
		_, _ = rand.Read(b[:])
		return 100000000 + binary.BigEndian.Uint32(b[:])%899999999
	}
	return uint32(n.Int64() + 100000000)
}

// randomTag 生成 SIP From tag（hex 随机串）。
func randomTag() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// noopWriter 丢弃帧（sink 未配置时）。
type noopWriter struct{}

func (noopWriter) WriteFrame([]byte, uint64) error { return nil }
func (noopWriter) Close()                          {}
