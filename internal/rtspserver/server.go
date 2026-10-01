// Package rtspserver 提供内嵌的 RTSP 服务器（基于 gortsplib，mediamtx 同款底层库）：
// 接收 ffmpeg 的推流（ANNOUNCE/RECORD），并向多个读者（DESCRIBE/SETUP/PLAY）分发，
// 用于替代独立部署的 mediamtx，实现纯单二进制部署。
package rtspserver

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

// DefaultPublisherWait 读者等待发布者就绪的默认时长。
const DefaultPublisherWait = 5 * time.Second

// TapFunc 订阅发布者 RTP 包的分发旁路。
// 回调在服务器 goroutine 上执行，禁止在其中同步调用 cancel（应通过 channel 通知）。
type TapFunc func(medi *description.Media, forma format.Format, pkt *rtp.Packet)

// ErrNoPublisher 指定路径在等待时限内没有发布者。
var ErrNoPublisher = fmt.Errorf("no rtsp publisher for path")

// tap 一个订阅者。
type tap struct {
	id     uint64
	fn     TapFunc
	onGone func()
}

// pathEntry 一个路径（/cam/<id>）对应的流与发布者。
type pathEntry struct {
	stream    *gortsplib.ServerStream
	publisher *gortsplib.ServerSession

	taps     map[uint64]*tap
	nextTap  uint64
}

// Server 内嵌 RTSP 服务器。生命周期：Start 后阻塞于 Wait；Close 停止一切。
type Server struct {
	srv    *gortsplib.Server
	logger *slog.Logger

	// PublisherWait 控制读者在发布者未就绪时的等待时长。
	PublisherWait time.Duration

	mu     sync.Mutex
	paths  map[string]*pathEntry
	closed bool
}

// New 创建内嵌 RTSP 服务器。
// rtspAddr 形如 ":8554"；udpRTPAddr/udpRTCPAddr 为空表示仅支持 TCP 传输。
func New(rtspAddr, udpRTPAddr, udpRTCPAddr string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		logger:        logger,
		PublisherWait: DefaultPublisherWait,
		paths:         map[string]*pathEntry{},
	}
	s.srv = &gortsplib.Server{
		Handler:     s,
		RTSPAddress: rtspAddr,
	}
	if udpRTPAddr != "" {
		s.srv.UDPRTPAddress = udpRTPAddr
		s.srv.UDPRTCPAddress = udpRTCPAddr
	}
	return s
}

// Start 启动监听。
func (s *Server) Start() error { return s.srv.Start() }

// Wait 阻塞直到服务器出错或被 Close。
func (s *Server) Wait() error { return s.srv.Wait() }

// Close 停止服务器并断开全部会话。
func (s *Server) Close() {
	type closed struct {
		e    *pathEntry
		taps map[uint64]*tap
	}
	var closings []closed
	s.mu.Lock()
	s.closed = true
	for path, e := range s.paths {
		if e.stream != nil {
			e.stream.Close()
		}
		if len(e.taps) > 0 {
			closings = append(closings, closed{e, e.taps})
			e.taps = nil
		}
		delete(s.paths, path)
	}
	s.mu.Unlock()
	for _, c := range closings {
		for _, t := range c.taps {
			if t.onGone != nil {
				go t.onGone()
			}
		}
	}
	s.srv.Close()
}

// lookup 等待指定路径的流就绪，超时返回 nil。
func (s *Server) lookup(path string, timeout time.Duration) *pathEntry {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		e, ok := s.paths[path]
		closed := s.closed
		s.mu.Unlock()
		if ok {
			return e
		}
		if closed || time.Now().After(deadline) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Subscribe 订阅指定路径发布者的 RTP 包（GB28181 点播推流的数据源）。
// 等待发布者就绪最长 timeout；超时返回 ErrNoPublisher。
// 发布者离开、被替换或服务器关闭时调用一次 onGone，订阅随之失效
// （需重新 Subscribe 等待新发布者）。返回的 cancel 用于主动退订，可重复调用。
func (s *Server) Subscribe(path string, timeout time.Duration, fn TapFunc, onGone func()) (cancel func(), err error) {
	if fn == nil {
		return nil, fmt.Errorf("tap fn is nil")
	}
	e := s.lookup(path, timeout)
	if e == nil {
		return nil, ErrNoPublisher
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrNoPublisher
	}
	id := e.nextTap
	e.nextTap++
	if e.taps == nil {
		e.taps = map[uint64]*tap{}
	}
	t := &tap{id: id, fn: fn, onGone: onGone}
	e.taps[id] = t
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if cur, ok := s.paths[path]; ok && cur == e {
				delete(e.taps, id)
			}
			s.mu.Unlock()
		})
	}, nil
}

// dispatchTaps 将发布者 RTP 包分发到全部订阅者。
// 回调在锁外执行；tap 集合通常为 0~2 个，逐包拷贝指针开销可忽略。
func (s *Server) dispatchTaps(e *pathEntry, medi *description.Media, forma format.Format, pkt *rtp.Packet) {
	s.mu.Lock()
	if len(e.taps) == 0 {
		s.mu.Unlock()
		return
	}
	fns := make([]TapFunc, 0, len(e.taps))
	for _, t := range e.taps {
		fns = append(fns, t.fn)
	}
	s.mu.Unlock()
	for _, fn := range fns {
		fn(medi, forma, pkt)
	}
}

// goneTaps 通知并清空路径上的全部订阅者。
func (s *Server) goneTaps(e *pathEntry) {
	s.mu.Lock()
	taps := e.taps
	e.taps = nil
	s.mu.Unlock()
	for _, t := range taps {
		if t.onGone != nil {
			t.onGone()
		}
	}
}

// ---- ServerHandler 实现 ----

// OnConnOpen 连接建立。
func (s *Server) OnConnOpen(_ *gortsplib.ServerHandlerOnConnOpenCtx) {}

// OnConnClose 连接关闭。
func (s *Server) OnConnClose(ctx *gortsplib.ServerHandlerOnConnCloseCtx) {
	s.logger.Debug("rtsp conn closed", "err", fmt.Sprint(ctx.Error))
}

// OnSessionOpen 会话建立。
func (s *Server) OnSessionOpen(_ *gortsplib.ServerHandlerOnSessionOpenCtx) {}

// OnSessionClose 会话关闭；发布者离开时释放对应路径（读者随之断开，
// 客户端会自动重连，配合 ffmpeg 退避重推实现秒级恢复，与 mediamtx 语义一致）。
func (s *Server) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	s.mu.Lock()
	var gone *pathEntry
	for path, e := range s.paths {
		if e.publisher == ctx.Session {
			if e.stream != nil {
				e.stream.Close()
			}
			delete(s.paths, path)
			gone = e
			s.logger.Info("rtsp publisher gone", "path", path)
		}
	}
	s.mu.Unlock()
	if gone != nil {
		s.goneTaps(gone)
	}
}

// OnDescribe 读者描述请求：返回该路径的流；发布者未就绪时等待。
func (s *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	e := s.lookup(ctx.Path, s.PublisherWait)
	if e == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	s.logger.Debug("rtsp describe", "path", ctx.Path)
	return &base.Response{StatusCode: base.StatusOK}, e.stream, nil
}

// OnAnnounce 发布者通告：为路径创建流；已有发布者则踢旧接管（ffmpeg 重启场景）。
func (s *Server) OnAnnounce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("server is closing")
	}
	var gone *pathEntry
	if old, ok := s.paths[ctx.Path]; ok {
		if old.publisher != nil && old.publisher != ctx.Session {
			old.publisher.Close()
		}
		if old.stream != nil {
			old.stream.Close()
		}
		delete(s.paths, ctx.Path)
		gone = old
	}
	stream := &gortsplib.ServerStream{
		Server: s.srv,
		Desc:   ctx.Description,
	}
	if err := stream.Initialize(); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("initialize stream: %w", err)
	}
	s.paths[ctx.Path] = &pathEntry{stream: stream, publisher: ctx.Session}
	s.mu.Unlock()
	if gone != nil {
		s.goneTaps(gone)
	}
	s.logger.Info("rtsp publisher announced",
		"path", ctx.Path, "medias", len(ctx.Description.Medias))
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// OnSetup 会话参数协商：发布者直接放行；读者返回对应路径的流。
func (s *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if ctx.Session.State() == gortsplib.ServerSessionStatePreRecord {
		return &base.Response{StatusCode: base.StatusOK}, nil, nil
	}
	e := s.lookup(ctx.Path, s.PublisherWait)
	if e == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, e.stream, nil
}

// OnPlay 读者开始播放。
func (s *Server) OnPlay(_ *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// OnRecord 发布者开始录制：把发布者的 RTP 包广播给全部读者与订阅者。
func (s *Server) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	s.mu.Lock()
	e, ok := s.paths[ctx.Path]
	s.mu.Unlock()
	if !ok || e.publisher != ctx.Session {
		return nil, fmt.Errorf("no announce for path %q", ctx.Path)
	}
	ctx.Session.OnPacketRTPAny(func(medi *description.Media, forma format.Format, pkt *rtp.Packet) {
		if err := e.stream.WritePacketRTP(medi, pkt); err != nil {
			s.logger.Warn("rtsp distribute failed", "err", err.Error())
		}
		s.dispatchTaps(e, medi, forma, pkt)
	})
	s.logger.Info("rtsp publisher recording", "path", ctx.Path)
	return &base.Response{StatusCode: base.StatusOK}, nil
}
