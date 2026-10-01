// Package stream 管理每个摄像头的 ffmpeg 取流进程：
// 拉取本地源（v4l2/avfoundation/dshow/rtsp/testsrc）转码 H264 后推送到内嵌 RTSP 服务器。
package stream

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
)

// Status 单个摄像头取流进程的运行状态。
type Status struct {
	Running   bool      `json:"running"`
	LastError string    `json:"last_error"`
	StartedAt time.Time `json:"started_at"`
	Restarts  int       `json:"restarts"`
}

// Runner 单个摄像头的 ffmpeg 进程监督器。
type Runner struct {
	cam      config.Camera
	pushAddr string
	bin      string
	logger   *slog.Logger

	mu      sync.Mutex
	status  Status
	spec    string
	stopCh  chan struct{}
	stopped bool
	tailErr string
}

// Manager 管理全部取流进程。
type Manager struct {
	mu       sync.Mutex
	runners  map[string]*Runner
	pushAddr string
	bin      string
	logger   *slog.Logger
}

// NewManager 创建取流管理器。pushAddr 形如 rtsp://127.0.0.1:8554。
func NewManager(pushAddr, bin string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		runners:  map[string]*Runner{},
		pushAddr: pushAddr,
		bin:      bin,
		logger:   logger,
	}
}

// Spec 描述摄像头取流的关键参数，任一变化即需要重启进程。
func Spec(c config.Camera) string {
	return fmt.Sprintf("%s|%s|%d|%d|%d|%d", c.Type, c.Source, c.Width, c.Height, c.FramerateOrDefault(), c.BitrateOrDefault())
}

// Sync 将运行态对齐到期望的摄像头列表：
// 新增的启动、删除/禁用的停止、参数变化的重启。
func (m *Manager) Sync(cameras []config.Camera) {
	m.mu.Lock()
	defer m.mu.Unlock()

	want := map[string]config.Camera{}
	for _, c := range cameras {
		if c.Enabled {
			want[c.ID] = c
		}
	}
	// 停掉不再需要的。
	for id, r := range m.runners {
		if _, ok := want[id]; !ok {
			r.Stop()
			delete(m.runners, id)
			continue
		}
		if r.Spec() != Spec(want[id]) {
			r.Stop()
			delete(m.runners, id)
		}
	}
	// 启动/保留需要的。
	for id, c := range want {
		if _, ok := m.runners[id]; ok {
			continue
		}
		r := newRunner(c, m.pushAddr, m.bin, m.logger)
		m.runners[id] = r
		go r.Run()
	}
}

// StopAll 停止全部进程。
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.runners {
		r.Stop()
		delete(m.runners, id)
	}
}

// Status 返回指定摄像头的取流状态。
func (m *Manager) Status(camID string) (Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runners[camID]
	if !ok {
		return Status{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, true
}

func newRunner(cam config.Camera, pushAddr, bin string, logger *slog.Logger) *Runner {
	return &Runner{
		cam:      cam,
		pushAddr: pushAddr,
		bin:      bin,
		logger:   logger,
		stopCh:   make(chan struct{}),
		spec:     Spec(cam),
	}
}

// Spec 返回创建时的参数指纹。
func (r *Runner) Spec() string { return r.spec }

// Stop 请求停止（幂等）。
func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stopped {
		r.stopped = true
		close(r.stopCh)
	}
}

func (r *Runner) isStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// Run 阻塞监督 ffmpeg 进程，异常退出后指数退避重启。
func (r *Runner) Run() {
	backoff := time.Second
	const maxBackoff = 15 * time.Second
	var starts int
	for !r.isStopped() {
		args := BuildArgs(r.cam, r.pushAddr+r.camPath())
		cmd := exec.Command(r.bin, args...) //nolint:gosec // 二进制路径来自受控配置
		var stderr tailBuffer
		cmd.Stderr = &stderr
		r.logger.Debug("ffmpeg starting", "camera", r.cam.ID, "args", args)
		r.setRunning(true, "", &starts)
		started := time.Now()
		err := cmd.Run()
		if r.isStopped() {
			r.setRunning(false, "", &starts)
			return
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if tail := stderr.Tail(); tail != "" {
			msg = msg + "; ffmpeg: " + tail
		}
		r.setRunning(false, msg, &starts)
		r.logger.Warn("ffmpeg exited, restarting", "camera", r.cam.ID, "err", msg,
			"uptime", time.Since(started).Round(time.Second).String())
		select {
		case <-r.stopCh:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (r *Runner) setRunning(v bool, errMsg string, starts *int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.Running = v
	r.status.LastError = errMsg
	if v {
		*starts++
		r.status.StartedAt = time.Now()
		r.status.Restarts = *starts - 1
	}
}

// tailBuffer 有界地记录 ffmpeg stderr，只保留末尾片段用于排障。
type tailBuffer struct {
	buf [4096]byte
	n   int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	if len(p) >= len(t.buf) {
		copy(t.buf[:], p[len(p)-len(t.buf):])
		t.n = len(t.buf)
		return len(p), nil
	}
	room := len(t.buf) - t.n
	if room < len(p) {
		copy(t.buf[:], t.buf[len(p)-room:])
		t.n = len(t.buf) - len(p)
	}
	copy(t.buf[t.n:], p)
	t.n += len(p)
	return len(p), nil
}

func (t *tailBuffer) Tail() string {
	return string(t.buf[:t.n])
}

func (r *Runner) camPath() string { return "/cam/" + r.cam.ID }

// InputArgs 按摄像头类型构造 ffmpeg 输入参数。
func InputArgs(c config.Camera) []string {
	fps := c.FramerateOrDefault()
	var args []string
	switch c.Type {
	case config.TypeV4L2:
		args = append(args, "-f", "v4l2")
		if fps > 0 {
			args = append(args, "-framerate", fmt.Sprintf("%d", fps))
		}
		if c.Width > 0 && c.Height > 0 {
			args = append(args, "-video_size", fmt.Sprintf("%dx%d", c.Width, c.Height))
		}
		args = append(args, "-i", c.Source)
	case config.TypeAVFoundation:
		args = append(args, "-f", "avfoundation")
		if fps > 0 {
			args = append(args, "-framerate", fmt.Sprintf("%d", fps))
		}
		src := c.Source
		if src == "" {
			src = "0"
		}
		args = append(args, "-i", src)
	case config.TypeDShow:
		args = append(args, "-f", "dshow")
		if fps > 0 {
			args = append(args, "-framerate", fmt.Sprintf("%d", fps))
		}
		if c.Width > 0 && c.Height > 0 {
			args = append(args, "-video_size", fmt.Sprintf("%dx%d", c.Width, c.Height))
		}
		src := strings.TrimSpace(c.Source)
		// 允许直接填设备名；已带 video= 前缀或别名（@device_...）时原样使用。
		if src != "" && !strings.Contains(src, "=") {
			src = "video=" + src
		}
		args = append(args, "-i", src)
	case config.TypeRTSP:
		args = append(args, "-rtsp_transport", "tcp", "-i", c.Source)
	case config.TypeTestSrc:
		w, h := 1280, 720
		if c.Width > 0 && c.Height > 0 {
			w, h = c.Width, c.Height
		}
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=%dx%d:rate=%d", w, h, fps))
	default:
		args = append(args, "-i", c.Source)
	}
	return args
}

// BuildArgs 构造完整的 ffmpeg 取流推流命令参数。
func BuildArgs(c config.Camera, pushURL string) []string {
	fps := c.FramerateOrDefault()
	gov := fps * 2
	if gov < 10 {
		gov = 10
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, InputArgs(c)...)
	args = append(args,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "zerolatency",
		"-bf", "0", // 禁用 B 帧：GB28181 PS 封装按 PTS=DTS 写 PES 头
		"-pix_fmt", "yuv420p",
		"-g", fmt.Sprintf("%d", gov),
		"-b:v", fmt.Sprintf("%dk", c.BitrateOrDefault()),
		"-an",
		"-f", "rtsp",
		"-rtsp_transport", "tcp",
		pushURL,
	)
	return args
}
