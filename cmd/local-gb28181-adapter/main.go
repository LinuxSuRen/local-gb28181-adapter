// local-gb28181-adapter 把本地摄像头（USB / macOS / RTSP / 测试源）暴露为国标
// GB/T 28181 虚拟设备：向上级平台注册、应答目录与云台控制、INVITE 点播时将
// H264 封装 PS over RTP 推流给平台，配套 Web 管理台。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/linuxsuren/local-gb28181-adapter/internal/api"
	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
	"github.com/linuxsuren/local-gb28181-adapter/internal/ptzmock"
	"github.com/linuxsuren/local-gb28181-adapter/internal/rtspserver"
	"github.com/linuxsuren/local-gb28181-adapter/internal/sig"
	"github.com/linuxsuren/local-gb28181-adapter/internal/snapshot"
	"github.com/linuxsuren/local-gb28181-adapter/internal/stream"
)

// version / commit / buildDate 由构建注入（-ldflags）。
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	var (
		httpAddr    = flag.String("http-addr", envOr("HTTP_ADDR", ":8080"), "HTTP 监听地址（UI/API）")
		advertiseIP = flag.String("advertise-ip", envOr("ADVERTISE_IP", ""), "对外宣告 IP，留空自动探测")
		rtspPush    = flag.String("rtsp-push", envOr("RTSP_PUSH", "rtsp://127.0.0.1:8554"), "ffmpeg 推流目标（仅 --rtsp-server=false 时使用，指向外部 mediamtx）")
		rtspPort    = flag.Int("rtsp-port", envInt("RTSP_PORT", 8554), "RTSP 端口（内嵌服务器监听端口）")
		rtspServer  = flag.Bool("rtsp-server", envBool("RTSP_SERVER", true), "启用内嵌 RTSP 服务器；关闭后按 --rtsp-push 推给外部 mediamtx")
		dataDir     = flag.String("data-dir", envOr("DATA_DIR", "./data"), "配置持久化目录")
		ffmpegBin   = flag.String("ffmpeg-bin", envOr("FFMPEG_BIN", "ffmpeg"), "ffmpeg 可执行文件路径")
		logLevel    = flag.String("log-level", envOr("LOG_LEVEL", "info"), "日志级别 debug|info|warn|error")
		showVersion = flag.Bool("version", false, "打印版本并退出")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("local-gb28181-adapter %s (commit %s, built %s)\n", version, commit, buildDate)
		return
	}

	logger := newLogger(*logLevel)
	slog.SetDefault(logger)

	store, err := config.LoadStore(*dataDir)
	if err != nil {
		logger.Error("load config failed", "err", err.Error())
		os.Exit(1)
	}

	// 命令行/环境变量值写回配置，使其成为唯一事实来源。
	if err := store.Update(func(r *config.Root) error {
		r.Server.HTTPAddr = *httpAddr
		r.Server.AdvertiseIP = *advertiseIP
		r.Server.RTSPPushAddr = *rtspPush
		r.Server.RTSPPort = *rtspPort
		r.Server.RTSPEmbedded = *rtspServer
		r.Server.FFmpegBin = *ffmpegBin
		return nil
	}); err != nil {
		logger.Error("save config failed", "err", err.Error())
		os.Exit(1)
	}
	root := store.Root()

	ip := root.Server.AdvertiseIP
	if ip == "" {
		ip = detectAdvertiseIP(logger)
		if err := store.Update(func(r *config.Root) error { r.Server.AdvertiseIP = ip; return nil }); err != nil {
			logger.Warn("persist advertise ip failed", "err", err.Error())
		}
	}

	// 端口漂移：配置端口被占用时向后尝试，直到能监听为止。
	ln, httpPort, drifted, err := listenWithDrift(root.Server.HTTPAddr, maxPortDrift, logger)
	if err != nil {
		logger.Error("http listen failed", "err", err.Error())
		os.Exit(1)
	}
	if drifted {
		logger.Warn("http port drifted", "from", root.Server.HTTPAddr, "to", ln.Addr().String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 内嵌 RTSP 服务器（gortsplib）：接收 ffmpeg 推流并向读者/点播会话分发。
	rtspPortActual := root.Server.RTSPPort
	pushAddr := root.Server.RTSPPushAddr
	var rtspSrv *rtspserver.Server
	if root.Server.RTSPEmbedded {
		rtspSrv, rtspPortActual, err = startEmbeddedRTSP(root.Server.RTSPPort, logger)
		if err != nil {
			logger.Error("embedded rtsp server failed", "err", err.Error())
			os.Exit(1)
		}
		if rtspPortActual != root.Server.RTSPPort {
			logger.Warn("rtsp port drifted", "from", root.Server.RTSPPort, "to", rtspPortActual)
		}
		pushAddr = fmt.Sprintf("rtsp://127.0.0.1:%d", rtspPortActual)
		go func() {
			if err := rtspSrv.Wait(); err != nil {
				logger.Error("embedded rtsp server exited", "err", err.Error())
			}
		}()
		logger.Info("embedded rtsp server started", "port", rtspPortActual)
	}

	ptzReg := ptzmock.NewRegistry()
	streams := stream.NewManager(pushAddr, root.Server.FFmpegBin, logger)
	snaps := snapshot.New(root.Server.FFmpegBin, logger)

	gb := sig.New(store, ptzReg, rtspSrv, streams, logger)
	gb.AdvertiseIP = ip
	if err := gb.Start(ctx, root.Platform.LocalAddr); err != nil {
		// 配置问题（如平台地址为空）不阻断启动：管理台仍可用，修正配置后保存即自动重载。
		logger.Error("gb28181 signaling start failed; fix platform config in web UI",
			"err", err.Error())
	}

	apiSrv := api.NewServer(store, ptzReg, streams, snaps, gb, version, logger)
	apiSrv.RTSPBaseURL = pushAddr
	apiSrv.OnPlatformChange = func() {
		if err := gb.Reconfigure(ctx); err != nil {
			logger.Error("gb28181 reconfigure failed", "err", err.Error())
		}
	}

	httpServer := &http.Server{
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go streams.Sync(store.Root().Cameras)

	go func() {
		logger.Info("http server starting",
			"addr", ln.Addr().String(),
			"ui", fmt.Sprintf("http://%s:%d/", ip, httpPort),
			"rtsp", fmt.Sprintf("rtsp://%s:%d", ip, rtspPortActual))
		if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "err", err.Error())
			cancel()
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	logger.Info("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	gb.Stop()
	streams.StopAll()
	if rtspSrv != nil {
		rtspSrv.Close()
	}
	logger.Info("stopped")
}

// ---- 辅助 ----

// 内嵌 RTSP 服务器的 UDP 传输端口（与 mediamtx 默认一致）。
const (
	rtspUDPPortRTP  = 8000
	rtspUDPPortRTCP = 8001
)

// startEmbeddedRTSP 启动内嵌 RTSP 服务器：
// TCP 端口被占用时向后漂移（与 HTTP 一致）；UDP 端口被占用时降级为仅 TCP。
func startEmbeddedRTSP(basePort int, logger *slog.Logger) (*rtspserver.Server, int, error) {
	for i := 0; i < maxPortDrift; i++ {
		port := basePort + i
		tcpAddr := fmt.Sprintf(":%d", port)
		if !tcpAddrFree(tcpAddr) {
			logger.Warn("rtsp port busy, drifting to next", "port", port)
			continue
		}
		udpRTP, udpRTCP := "", ""
		if udpPortsFree(rtspUDPPortRTP, rtspUDPPortRTCP) {
			udpRTP = fmt.Sprintf(":%d", rtspUDPPortRTP)
			udpRTCP = fmt.Sprintf(":%d", rtspUDPPortRTCP)
		} else {
			logger.Warn("rtsp udp ports busy, tcp-only mode", "ports", fmt.Sprintf("%d/%d", rtspUDPPortRTP, rtspUDPPortRTCP))
		}
		srv := rtspserver.New(tcpAddr, udpRTP, udpRTCP, logger)
		if err := srv.Start(); err != nil {
			srv.Close()
			return nil, 0, fmt.Errorf("rtsp server start on %s: %w", tcpAddr, err)
		}
		return srv, port, nil
	}
	return nil, 0, fmt.Errorf("rtsp ports %d-%d all busy", basePort, basePort+maxPortDrift-1)
}

// tcpAddrFree 探测 TCP 地址是否可绑定（探测后立即释放，存在微小竞争窗口）。
func tcpAddrFree(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	return ln.Close() == nil
}

// udpPortsFree 探测 UDP 端口是否可绑定。
func udpPortsFree(ports ...int) bool {
	for _, p := range ports {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: p})
		if err != nil {
			return false
		}
		_ = conn.Close()
	}
	return true
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return v == "1" || v == "true" || v == "yes"
	}
	return def
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

// detectAdvertiseIP 探测对外 IP：优先 UDP 拨号探测默认路由，失败则取首个非环回 IPv4。
func detectAdvertiseIP(logger *slog.Logger) string {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err == nil {
		defer func() { _ = conn.Close() }()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil && !addr.IP.IsLoopback() {
			return addr.IP.String()
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		logger.Warn("detect advertise ip failed, fallback to 127.0.0.1", "err", err.Error())
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() {
			return ipNet.IP.String()
		}
	}
	return "127.0.0.1"
}

// maxPortDrift 端口被占用时最多向后尝试的端口个数（含首选端口）。
const maxPortDrift = 20

// listenWithDrift 监听 addr 的 TCP 端口；端口被占用（EADDRINUSE）时依次向后
// 尝试下一个端口，直到成功或用尽 maxAttempts 个候选。返回监听器、实际端口、
// 是否发生了漂移。非占用类错误（如权限不足）直接返回，不做漂移。
func listenWithDrift(addr string, maxAttempts int, logger *slog.Logger) (net.Listener, int, bool, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, 0, false, fmt.Errorf("invalid http addr %q (want host:port): %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return nil, 0, false, fmt.Errorf("invalid http port %q in %q", portStr, addr)
	}
	for i := 0; i < maxAttempts; i++ {
		candidate := port + i
		if candidate > 65535 {
			break
		}
		laddr := net.JoinHostPort(host, strconv.Itoa(candidate))
		ln, err := net.Listen("tcp", laddr)
		if err == nil {
			return ln, candidate, i > 0, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, 0, false, fmt.Errorf("listen %s: %w", laddr, err)
		}
		logger.Warn("http port busy, drifting to next", "busy", laddr, "next", candidate+1)
	}
	return nil, 0, false, fmt.Errorf("ports %d-%d all busy on %s", port, port+maxAttempts-1, host)
}
