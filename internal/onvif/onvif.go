// Package onvif 把国标通道虚拟为 ONVIF 设备对外提供服务：
// Device / Media / PTZ 三个服务的 SOAP 接口 + WS-Discovery 发现。
// 外部程序（NVR / VMS / 支持 ONVIF 的播放器）只对接本适配器，
// 即可间接取流与控制国标下级相机。
package onvif

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
	"github.com/linuxsuren/local-gb28181-adapter/internal/uplevel"
)

// Server ONVIF 服务。挂载在管理 HTTP 端口上（/onvif/ 前缀），无独立监听。
type Server struct {
	store  *config.Store
	up     *uplevel.Service
	logger *slog.Logger

	mu       sync.Mutex
	baseAddr string // 对外宣告基地址，如 http://192.168.18.165:18080
	host     string // 宣告主机名（不含端口）
	rtspPort int
}

// New 创建 ONVIF 服务。
func New(store *config.Store, up *uplevel.Service, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{store: store, up: up, logger: logger}
}

// SetEndpoints 设置对外宣告的基地址与 RTSP 端口（拼接 StreamUri 用）。
func (s *Server) SetEndpoints(baseURL string, rtspPort int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseAddr = strings.TrimRight(baseURL, "/")
	s.rtspPort = rtspPort
	// 提取宣告主机名（剥离 scheme 与端口），RTSP 地址用同一主机 + 独立 RTSP 端口。
	hostPort := strings.TrimPrefix(strings.TrimPrefix(s.baseAddr, "http://"), "https://")
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		s.host = h
	} else {
		s.host = hostPort
	}
}

// Register 挂载 ONVIF 路由到现有 mux。
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /onvif/{channel}/device_service", s.handleDeviceService)
	mux.HandleFunc("GET /onvif/device_list", s.handleDeviceList)
}

// handleDeviceList 调试辅助：列出全部通道的 ONVIF 服务入口。
func (s *Server) handleDeviceList(w http.ResponseWriter, _ *http.Request) {
	var b strings.Builder
	b.WriteString("[\n")
	for _, dev := range s.up.Registry.Snapshot() {
		for _, ch := range dev.Channels {
			fmt.Fprintf(&b, "  {\"channel\": %q, \"name\": %q, \"device_service\": \"/onvif/%s/device_service\"},\n",
				ch.DeviceID, ch.Name, ch.DeviceID)
		}
	}
	b.WriteString("]")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(b.String()))
}

// ---- 入口与分发 ----

// handleDeviceService 解析 SOAP 请求体，按 Body 首子元素分发到对应服务。
func (s *Server) handleDeviceService(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channel")
	if !s.validChannel(channelID) {
		writeSOAPFault(w, "unknown channel "+channelID)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeSOAPFault(w, "read body: "+err.Error())
		return
	}
	action, payload := parseSOAPAction(string(body))
	s.logger.Debug("onvif request", "channel", channelID, "action", action)

	var resp string
	switch action {
	case "GetSystemDateAndTime":
		resp = respGetSystemDateAndTime
	case "GetDeviceInformation":
		resp = s.respGetDeviceInformation(channelID)
	case "GetCapabilities":
		resp = s.respGetCapabilities(channelID)
	case "GetServices":
		resp = s.respGetServices(channelID)
	case "GetProfiles", "GetProfile":
		resp = s.respGetProfiles(channelID)
	case "GetStreamUri":
		// 拉流前自动发起 GB 点播（外部程序无需任何手动操作）。
		go func() {
			ctx, cancel := contextTimeout(20 * time.Second)
			defer cancel()
			if _, err := s.up.Play(ctx, channelID); err != nil {
				s.logger.Warn("onvif autoplay failed", "channel", channelID, "err", err.Error())
			}
		}()
		resp = s.respGetStreamUri(channelID)
	case "GetSnapshotUri":
		resp = s.respGetSnapshotUri(channelID)
	case "GetVideoEncoderConfigurations", "GetVideoEncoderConfiguration",
		"GetVideoSourceConfigurations", "GetVideoSourceConfiguration",
		"GetVideoEncoderConfigurationOptions":
		resp = s.respVideoConfigs(channelID)
	case "ContinuousMove":
		resp = s.handleContinuousMove(channelID, payload)
	case "Stop":
		resp = s.handlePTZStop(channelID, payload)
	case "GetStatus":
		resp = respPTZStatus
	case "GetNodes":
		resp = s.respPTZNodes(channelID)
	case "GetConfigurations", "GetPTZConfigurations":
		resp = s.respPTZConfigurations(channelID)
	case "GetServiceCapabilities":
		resp = respPTZServiceCapabilities
	default:
		s.logger.Debug("onvif action not implemented, reply ok", "action", action)
		resp = fmt.Sprintf(respGenericOK, action)
	}
	resp = finalizePlaceholders(resp)
	w.Header().Set("Content-Type", `application/soap+xml; charset=utf-8`)
	_, _ = w.Write([]byte(resp))
}

// finalizePlaceholders 替换响应里的时间占位符。
func finalizePlaceholders(resp string) string {
	if !strings.Contains(resp, "{{") {
		return resp
	}
	now := time.Now().UTC()
	r := strings.NewReplacer(
		"{{Y}}", fmt.Sprintf("%d", now.Year()),
		"{{MO}}", fmt.Sprintf("%d", int(now.Month())),
		"{{D}}", fmt.Sprintf("%d", now.Day()),
		"{{H}}", fmt.Sprintf("%d", now.Hour()),
		"{{M}}", fmt.Sprintf("%d", now.Minute()),
		"{{S}}", fmt.Sprintf("%d", now.Second()),
		"{{NOW}}", now.Format("2006-01-02T15:04:05Z"),
	)
	return r.Replace(resp)
}

// validChannel 通道是否存在于某设备目录。
func (s *Server) validChannel(channelID string) bool {
	_, _, ok := s.up.Registry.FindByChannel(channelID)
	return ok
}

// ---- URL 拼接 ----

func (s *Server) serviceURL(channelID string) string {
	return fmt.Sprintf("%s/onvif/%s/device_service", s.baseURL(), channelID)
}

func (s *Server) streamURL(channelID string) string {
	s.mu.Lock()
	host, port := s.host, s.rtspPort
	s.mu.Unlock()
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("rtsp://%s:%d/gb/%s", host, port, channelID)
}

func (s *Server) snapshotURL(channelID string) string {
	return fmt.Sprintf("%s/api/uplevel/channels/%s/snapshot", s.baseURL(), channelID)
}

// hostPort 从 baseURL 提取 host:port，空则 127.0.0.1:18080。
func (s *Server) hostPort() string {
	u := strings.TrimPrefix(strings.TrimPrefix(s.baseURL(), "http://"), "https://")
	if u == "" {
		return "127.0.0.1:18080"
	}
	return u
}

func (s *Server) baseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseAddr
}

// channelInfo 返回通道与其所属设备信息。
func (s *Server) channelInfo(channelID string) (uplevel.ChannelInfo, uplevel.Device, bool) {
	dev, ch, ok := s.up.Registry.FindByChannel(channelID)
	return ch, dev, ok
}

func contextTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
