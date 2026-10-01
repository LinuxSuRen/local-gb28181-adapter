package sig

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph264"
	"github.com/emiago/sipgo"
	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
	"github.com/linuxsuren/local-gb28181-adapter/internal/gbsdp"
	"github.com/linuxsuren/local-gb28181-adapter/internal/psrtp"
	"github.com/pion/rtp"
)

// H264 NAL 类型。
const (
	nalTypeSPS = 7
	nalTypePPS = 8
	nalTypeIDR = 5
)

// session 一次点播会话：从内嵌 RTSP 服务器订阅摄像头流，
// H264 → PS 封装 → RTP 发往平台媒体端口。
type session struct {
	svc       *Service
	dlg       *sipgo.DialogServerSession
	cam       config.Camera
	offer     gbsdp.Offer
	sender    *psrtp.Sender
	channelID string

	mu        sync.Mutex
	confirmed atomic.Bool
	closed    atomic.Bool
	started   time.Time

	cancelTap func()
	goneCh    chan struct{}
	stopCh    chan struct{}

	// 解码状态（tap 回调线程内独占访问）
	dec     *rtph264.Decoder
	h264Fmt *format.H264
	muxer   *psrtp.PSMuxer
	sentAny bool
}

func newSession(svc *Service, dlg *sipgo.DialogServerSession, cam config.Camera,
	offer gbsdp.Offer, sender *psrtp.Sender, channelID string) *session {
	return &session{
		svc:       svc,
		dlg:       dlg,
		cam:       cam,
		offer:     offer,
		sender:    sender,
		channelID: channelID,
		started:   time.Now(),
		goneCh:    make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		muxer:     psrtp.NewPSMuxer(),
	}
}

// info 会话快照。
func (s *session) info() SessionInfo {
	return SessionInfo{
		CallID:    s.dlg.ID,
		ChannelID: s.channelID,
		CameraID:  s.cam.ID,
		Transport: string(s.offer.Transport),
		Remote:    s.sender.RemoteAddr(),
		StartedAt: s.started,
		Confirmed: s.confirmed.Load(),
	}
}

// start 订阅取流并发送。发布者离开时自动等待新发布者（ffmpeg 重启），
// 直到会话被 BYE/关闭。
func (s *session) start() {
	for {
		if s.closed.Load() {
			return
		}
		wait := s.svc.PublisherWait
		if wait <= 0 {
			wait = 10 * time.Second
		}
		cancel, err := s.svc.rtsp.Subscribe("/cam/"+s.cam.ID, wait, s.onPacket, s.onGone)
		if err != nil {
			// 无发布者：摄像头可能未启用或 ffmpeg 未就绪，等待后重试。
			s.svc.logger.Warn("gb28181 session wait publisher", "camera", s.cam.ID, "err", err.Error())
			select {
			case <-time.After(3 * time.Second):
				continue
			case <-s.stopCh:
				return
			}
		}
		s.mu.Lock()
		s.cancelTap = cancel
		s.mu.Unlock()

		select {
		case <-s.goneCh:
			// 发布者离开，退订后重新等待。
			cancel()
			s.svc.logger.Info("gb28181 session publisher gone, resubscribing", "camera", s.cam.ID)
			continue
		case <-s.stopCh:
			cancel()
			return
		}
	}
}

// close 关闭会话：退订、停发、断开。幂等。
func (s *session) close(reason string) {
	if s.closed.Swap(true) {
		return
	}
	close(s.stopCh)
	s.mu.Lock()
	cancel := s.cancelTap
	s.cancelTap = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = s.sender.Close()
	s.svc.logger.Info("gb28181 session closed",
		"dialog", s.dlg.ID, "camera", s.cam.ID, "reason", reason)
}

// onGone 发布者离开回调（服务器 goroutine 上执行，必须非阻塞）。
func (s *session) onGone() {
	select {
	case s.goneCh <- struct{}{}:
	default:
	}
}

// onPacket 收到发布者 RTP 包：H264 解包 → PS 封装 → RTP 发送。
func (s *session) onPacket(medi *description.Media, forma format.Format, pkt *rtp.Packet) {
	if s.closed.Load() {
		return
	}
	h264f, ok := forma.(*format.H264)
	if !ok {
		return
	}
	if s.dec == nil || s.h264Fmt != h264f {
		dec, err := h264f.CreateDecoder()
		if err != nil {
			s.svc.logger.Warn("gb28181 h264 decoder init failed", "err", err.Error())
			return
		}
		s.dec = dec
		s.h264Fmt = h264f
		s.sentAny = false
	}
	nalus, err := s.dec.Decode(pkt)
	if err != nil {
		if !errors.Is(err, rtph264.ErrMorePacketsNeeded) {
			s.svc.logger.Debug("gb28181 h264 decode", "err", err.Error())
		}
		return
	}

	idr := false
	hasSPS := false
	for _, nalu := range nalus {
		switch nalu[0] & 0x1F {
		case nalTypeIDR:
			idr = true
		case nalTypeSPS:
			hasSPS = true
		}
	}
	// IDR 帧前补 SPS/PPS（ffmpeg 的 SDP sprop 参数不随码流下发）。
	if idr && !hasSPS {
		sps, pps := s.h264Fmt.SafeParams()
		if len(sps) > 0 && len(pps) > 0 {
			nalus = append([][]byte{sps, pps}, nalus...)
		}
	}
	// 首帧未从 IDR 开始时丢弃，避免平台解出花屏。
	if !s.sentAny && !idr {
		return
	}

	ps := s.muxer.PackAU(nalus, int64(pkt.Timestamp), int64(pkt.Timestamp), idr)
	if err := s.sender.SendPS(ps, pkt.Timestamp); err != nil {
		s.svc.logger.Warn("gb28181 send ps failed", "err", err.Error())
		return
	}
	s.sentAny = true
}
