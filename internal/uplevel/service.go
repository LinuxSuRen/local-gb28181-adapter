// Package uplevel 实现国标上级平台信令：
// 作为 SIP 服务器接收下级国标设备（如海康相机）的 REGISTER（digest 挑战认证）、
// 心跳保活与目录上报，支持主动查询目录/设备信息、INVITE 点播收流与云台控制。
package uplevel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
)

// FrameWriter 点播收流后的视频帧出口（由上层实现，如转发到内嵌 RTSP 服务器）。
type FrameWriter interface {
	// WriteFrame 写入一个 H264 AnnexB 访问单元（pts 为 90kHz 时钟）。
	WriteFrame(frame []byte, pts uint64) error
	// Close 关闭出口并释放资源。
	Close()
}

// StreamSink 为点播通道提供视频帧出口。
type StreamSink interface {
	// OpenChannel 打开通道的帧出口；同通道重复打开由调用方避免。
	OpenChannel(channelID string) (FrameWriter, error)
}

// StreamSinkFunc 函数适配器（http.HandlerFunc 风格）。
type StreamSinkFunc func(channelID string) (FrameWriter, error)

// OpenChannel 实现 StreamSink。
func (f StreamSinkFunc) OpenChannel(channelID string) (FrameWriter, error) { return f(channelID) }

// Service 上级平台信令服务。生命周期：Start 启动 SIP 监听，Stop 全量停止。
// 配置变更通过 Reconfigure 触发全量重启。
type Service struct {
	store  *config.Store
	sink   StreamSink
	logger *slog.Logger

	// AdvertiseIP 对外宣告 IP（INVITE SDP c= 行使用，指向本机收流端口）。
	AdvertiseIP string

	Registry *Registry

	mu        sync.Mutex
	running   bool
	ua        *sipgo.UserAgent
	srv       *sipgo.Server
	client    *sipgo.Client
	dialog    *sipgo.DialogUA
	sipCtx    context.Context
	sipCancel context.CancelFunc
	sipPort   int

	sessions map[string]*Session // channelID → 点播会话
	pending  map[string]chan any // 查询等待者（deviceID:sn）
	sn       atomic.Int64
}

// New 创建上级平台信令服务。sink 允许为 nil（点播收流仅记日志）。
func New(store *config.Store, sink StreamSink, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:    store,
		sink:     sink,
		logger:   logger,
		Registry: NewRegistry(),
		sessions: map[string]*Session{},
		pending:  map[string]chan any{},
	}
}

// maxSIPPortDrift SIP 端口漂移候选数（与设备端 sig 同策略）。
const maxSIPPortDrift = 20

// Start 启动 SIP 监听。端口被占用时向后漂移。
func (s *Service) Start(ctx context.Context) error {
	localAddr := s.store.Root().Uplevel.LocalAddr
	for i := 0; i < maxSIPPortDrift; i++ {
		err := s.startOnce(ctx, localAddr)
		if err == nil {
			return nil
		}
		if isAddrInUse(err) {
			host, port, perr := splitAddr(localAddr)
			if perr == nil && port > 0 {
				next := net.JoinHostPort(host, strconv.Itoa(port+1))
				s.logger.Warn("uplevel sip port busy, drifting", "next", next)
				localAddr = next
				continue
			}
		}
		return err
	}
	return fmt.Errorf("uplevel sip ports all busy")
}

// startOnce 尝试一次启动。
func (s *Service) startOnce(ctx context.Context, localAddr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	up := s.store.Root().Uplevel
	if !up.Enabled {
		s.logger.Info("uplevel platform disabled")
		return nil
	}
	host, port, err := splitAddr(localAddr)
	if err != nil {
		return fmt.Errorf("invalid uplevel local addr %q: %w", localAddr, err)
	}

	ua, err := sipgo.NewUA(sipgo.WithUserAgent(up.ServerID))
	if err != nil {
		return fmt.Errorf("create ua: %w", err)
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(firstNonEmpty(s.AdvertiseIP, host)),
		sipgo.WithClientPort(port))
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}
	srv.OnRegister(s.onRegister)
	srv.OnMessage(s.onMessage)
	srv.OnBye(s.onBye)

	sipCtx, cancel := context.WithCancel(ctx)
	listenErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(sipCtx, "udp", localAddr); err != nil && sipCtx.Err() == nil {
			listenErr <- err
		}
	}()
	select {
	case err := <-listenErr:
		cancel()
		_ = client.Close()
		return fmt.Errorf("uplevel sip listen %s: %w", localAddr, err)
	case <-time.After(300 * time.Millisecond):
	}

	s.ua, s.srv, s.client = ua, srv, client
	s.dialog = &sipgo.DialogUA{Client: client}
	s.sipCtx, s.sipCancel = sipCtx, cancel
	s.sipPort = port
	s.running = true

	// 心跳超时巡检（周期取 30s，超时阈值 3 分钟）。
	go s.keepaliveLoop(sipCtx)
	s.logger.Info("uplevel signaling started",
		"local", localAddr,
		"server_id", up.ServerID,
		"realm", up.Realm())
	return nil
}

// Stop 停止信令服务并断开全部点播会话。
func (s *Service) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	if s.sipCancel != nil {
		s.sipCancel()
	}
	for id, sess := range s.sessions {
		sess.close("service stop")
		delete(s.sessions, id)
	}
	client, srv := s.client, s.srv
	s.client, s.srv, s.dialog = nil, nil, nil
	s.mu.Unlock()

	if client != nil {
		_ = client.Close()
	}
	if srv != nil {
		_ = srv.Close()
	}
	s.logger.Info("uplevel signaling stopped")
}

// Reconfigure 配置变更后全量重启。
func (s *Service) Reconfigure(ctx context.Context) error {
	s.Stop()
	return s.Start(ctx)
}

// Running 是否在运行。
func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// LocalPort 返回实际监听端口（未运行为 0）。
func (s *Service) LocalPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sipPort
}

// nextSN 生成 MANSCDP 序列号。
func (s *Service) nextSN() int {
	return int(s.sn.Add(1) % 1000000)
}

// keepaliveLoop 心跳超时巡检。
func (s *Service) keepaliveLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Registry.SweepKeepalive(3 * time.Minute)
		}
	}
}

// ---- REGISTER：digest 挑战认证 ----

func (s *Service) onRegister(req *sip.Request, tx sip.ServerTransaction) {
	up := s.store.Root().Uplevel
	deviceID := ""
	if req.To() != nil && req.To().Address.User != "" {
		deviceID = req.To().Address.User
	}
	if deviceID == "" {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
		return
	}
	expires := 3600
	if e := req.GetHeader("Expires"); e != nil {
		if v, err := strconv.Atoi(e.Value()); err == nil {
			expires = v
		}
	}

	auth := req.GetHeader("Authorization")
	if auth == nil {
		// 第一跳：401 挑战。
		chal := digest.Challenge{
			Realm:     up.Realm(),
			Nonce:     fmt.Sprintf("%d", time.Now().UnixNano()),
			Algorithm: "MD5",
		}
		res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
		res.AppendHeader(sip.NewHeader("WWW-Authenticate", chal.String()))
		_ = tx.Respond(res)
		return
	}

	cred, err := digest.ParseCredentials(auth.Value())
	if err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Authorization", nil))
		return
	}
	chal := digest.Challenge{Realm: up.Realm(), Nonce: cred.Nonce, Algorithm: "MD5"}
	if cred.QOP != "" {
		chal.QOP = []string{cred.QOP}
	}
	want, err := digest.Digest(&chal, digest.Options{
		Method:   sip.REGISTER.String(),
		URI:      cred.URI,
		Username: cred.Username,
		Password: up.Password,
		Cnonce:   cred.Cnonce,
		Count:    cred.Nc,
	})
	if err != nil || cred.Response != want.Response {
		s.logger.Warn("uplevel register auth failed", "device", deviceID, "user", cred.Username)
		_ = tx.Respond(sip.NewResponseFromRequest(req, 401, "Unauthorized", nil))
		return
	}

	ip := "127.0.0.1"
	port := 5060
	if src := req.Source(); src != "" {
		if h, p, err := net.SplitHostPort(src); err == nil {
			ip = h
			if v, err := strconv.Atoi(p); err == nil {
				port = v
			}
		} else {
			ip = src
		}
	}

	if expires <= 0 {
		s.Registry.Unregister(deviceID)
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "0"))
		_ = tx.Respond(res)
		s.logger.Info("uplevel device unregistered", "device", deviceID, "remote", net.JoinHostPort(ip, strconv.Itoa(port)))
		return
	}

	d := s.Registry.Register(deviceID, ip, port, expires)
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	res.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))
	res.AppendHeader(sip.NewHeader("Date", time.Now().Format("2006-01-02T15:04:05.000")))
	_ = tx.Respond(res)
	s.logger.Info("uplevel device registered",
		"device", deviceID,
		"remote", net.JoinHostPort(d.RemoteIP, strconv.Itoa(d.RemotePort)),
		"expires", expires)

	// 目录为空时自动查询（新设备注册后 ONVIF/API 侧立即可用）。
	if len(d.Channels) == 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := s.QueryDeviceInfo(ctx, deviceID); err != nil {
				s.logger.Debug("uplevel auto deviceinfo failed", "device", deviceID, "err", err.Error())
			}
			if _, err := s.QueryCatalog(ctx, deviceID); err != nil {
				s.logger.Debug("uplevel auto catalog failed", "device", deviceID, "err", err.Error())
			}
		}()
	}
}

// ---- 辅助 ----

func splitAddr(addr string) (host string, port int, err error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err = strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("bad port %q", portStr)
	}
	return host, port, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return errors.Is(opErr.Err, syscall.EADDRINUSE)
	}
	return false
}
