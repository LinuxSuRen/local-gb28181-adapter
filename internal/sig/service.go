// Package sig 实现 GB/T 28181 设备端信令：
// 向上级平台注册（digest 认证）、心跳保活、目录/设备信息应答、
// PTZ 控制指令解析，以及 INVITE 点播会话的媒体推流。
package sig

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
	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
	"github.com/linuxsuren/local-gb28181-adapter/internal/ptzmock"
	"github.com/linuxsuren/local-gb28181-adapter/internal/rtspserver"
	"github.com/linuxsuren/local-gb28181-adapter/internal/stream"
)

// RegistrationState 注册状态。
type RegistrationState string

// 注册状态取值。
const (
	StateIdle         RegistrationState = "idle"         // 未启用或已停止
	StateRegistering  RegistrationState = "registering"  // 注册中（含认证重试）
	StateRegistered   RegistrationState = "registered"   // 在线
	StateUnregistered RegistrationState = "unregistered" // 注册失败/离线
)

// SessionInfo 点播会话信息（管理 API 展示用）。
type SessionInfo struct {
	CallID    string    `json:"call_id"`
	ChannelID string    `json:"channel_id"`
	CameraID  string    `json:"camera_id"`
	Transport string    `json:"transport"` // UDP / TCP
	Remote    string    `json:"remote"`    // 平台媒体接收地址
	StartedAt time.Time `json:"started_at"`
	Confirmed bool      `json:"confirmed"` // 是否已收到 ACK
}

// Service 国标信令服务。生命周期：Start 启动监听与注册循环，Stop 全量停止。
// 平台配置变更通过 Reconfigure 触发全量重启（Stop + Start）。
type Service struct {
	store   *config.Store
	ptz     *ptzmock.Registry
	rtsp    *rtspserver.Server
	streams *stream.Manager
	logger  *slog.Logger

	// AdvertiseIP 对外宣告 IP（SDP c= 行与 Contact 使用）。
	AdvertiseIP string
	// PublisherWait 点播时等待取流发布者（ffmpeg）就绪的时长。
	PublisherWait time.Duration

	mu        sync.Mutex
	running   bool
	ua        *sipgo.UserAgent
	srv       *sipgo.Server
	client    *sipgo.Client
	dialogUA  *sipgo.DialogUA
	sessions  map[string]*session
	sipCtx    context.Context
	sipCancel context.CancelFunc
	sipPort   int // 实际监听端口

	state       atomic.Value // RegistrationState
	lastRegAt   atomic.Value // time.Time
	lastKeep    atomic.Value // time.Time
	keepFails   atomic.Int32
	sn          atomic.Int64
	regErr      atomic.Value // string
}

// New 创建信令服务。
func New(store *config.Store, ptz *ptzmock.Registry, rtsp *rtspserver.Server,
	streams *stream.Manager, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		store:   store,
		ptz:     ptz,
		rtsp:    rtsp,
		streams: streams,
		logger:  logger,
		sessions: map[string]*session{},
		PublisherWait: 10 * time.Second,
	}
	s.state.Store(StateIdle)
	return s
}

// Start 启动 SIP 监听与注册循环。已在运行时为 no-op。
// localAddr 形如 ":5060"；端口被占用时向后漂移（与 HTTP/RTSP 同策略，最多 20 个）。
func (s *Service) Start(ctx context.Context, localAddr string) error {
	for i := 0; i < maxSIPPortDrift; i++ {
		err := s.startOnce(ctx, localAddr)
		if err == nil {
			return nil
		}
		if isAddrInUse(err) {
			host, port, perr := splitAddr(localAddr)
			if perr == nil && port > 0 {
				next := net.JoinHostPort(host, strconv.Itoa(port+1))
				s.logger.Warn("sip port busy, drifting", "next", next)
				localAddr = next
				continue
			}
		}
		return err
	}
	return fmt.Errorf("sip ports all busy")
}

// maxSIPPortDrift SIP 端口漂移候选数。
const maxSIPPortDrift = 20

// startOnce 尝试一次启动；端口占用返回 isAddrInUse 可识别的错误。
func (s *Service) startOnce(ctx context.Context, localAddr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	root := s.store.Root()
	if !root.Platform.Enabled {
		s.logger.Info("gb28181 platform disabled")
		return nil
	}
	if root.Platform.ServerAddr == "" {
		return fmt.Errorf("platform server_addr is empty")
	}

	host, port, err := splitAddr(localAddr)
	if err != nil {
		return fmt.Errorf("invalid sip local addr %q: %w", localAddr, err)
	}

	ua, err := sipgo.NewUA(sipgo.WithUserAgent(root.Platform.DeviceID))
	if err != nil {
		return fmt.Errorf("create ua: %w", err)
	}
	// 对外 IP 与端口写入 client，Via/Contact 的地址由传输层补全。
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(firstNonEmpty(s.AdvertiseIP, host)),
		sipgo.WithClientPort(port))
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}
	srv.OnMessage(s.onMessage)
	srv.OnInvite(s.onInvite)
	srv.OnAck(s.onAck)
	srv.OnBye(s.onBye)

	sipCtx, cancel := context.WithCancel(ctx)
	listenErr := make(chan error, 1)
	go func() {
		// ListenAndServe 阻塞直到出错或 ctx 取消；
		// 端口占用会立即报错。
		if err := srv.ListenAndServe(sipCtx, "udp", localAddr); err != nil && sipCtx.Err() == nil {
			listenErr <- err
		}
	}()
	select {
	case err := <-listenErr:
		cancel()
		_ = client.Close()
		return fmt.Errorf("sip listen %s: %w", localAddr, err)
	case <-time.After(300 * time.Millisecond):
		// 监听已就绪（UDP ListenAndServe 立即成功则不返回）。
	}

	s.ua, s.srv, s.client = ua, srv, client
	s.dialogUA = &sipgo.DialogUA{Client: client}
	s.sipCtx, s.sipCancel = sipCtx, cancel
	s.sipPort = port
	s.running = true
	s.state.Store(StateRegistering)
	s.regErr.Store("")

	go s.registerLoop(sipCtx)
	s.logger.Info("gb28181 signaling started",
		"local", localAddr,
		"platform", root.Platform.ServerID,
		"server", root.Platform.ServerAddr,
		"device", root.Platform.DeviceID)
	return nil
}

// Stop 停止信令服务并断开全部点播会话。未运行时为 no-op。
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
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
	if s.client != nil {
		_ = s.client.Close()
	}
	if s.srv != nil {
		_ = s.srv.Close()
	}
	s.state.Store(StateIdle)
	s.logger.Info("gb28181 signaling stopped")
}

// Reconfigure 平台配置变更后调用：全量重启信令栈。
func (s *Service) Reconfigure(ctx context.Context) error {
	s.Stop()
	return s.Start(ctx, s.store.Root().Platform.LocalAddr)
}

// Running 返回服务是否在运行。
func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Status 返回注册与心跳状态。
func (s *Service) Status() (state RegistrationState, lastRegister, lastKeepalive time.Time, keepaliveFails int32, lastErr string) {
	st, _ := s.state.Load().(RegistrationState)
	t1, _ := s.lastRegAt.Load().(time.Time)
	t2, _ := s.lastKeep.Load().(time.Time)
	e, _ := s.regErr.Load().(string)
	return st, t1, t2, s.keepFails.Load(), e
}

// Sessions 返回当前点播会话快照。
func (s *Service) Sessions() []SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionInfo, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess.info())
	}
	return out
}

// nextSN 生成 MANSCDP 序列号。
func (s *Service) nextSN() int {
	return int(s.sn.Add(1) % 1000000)
}

// ---- 内部会话登记 ----

func (s *Service) addSession(id string, sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = sess
}

func (s *Service) takeSession(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
	}
	return sess
}

func (s *Service) getSession(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
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
