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

// svcReq 一次 ONVIF 请求的解析结果：一台虚拟相机（物理设备）及其全部通道。
// profile_N 按通道顺序映射（profile_1 = 第一通道，通常可见光；红外通道
// 的 profile 名称带 infrared 后缀）。
type svcReq struct {
	id       string                  // 路径 ID（设备编码或通道编码）
	deviceID string                  // 所属设备编码
	channels []uplevel.ChannelInfo   // 通道列表（设备模式为全部通道）
}

// channelByToken 按 ProfileToken（profile_N）解析通道，N 从 1 起。
func (r svcReq) channelByToken(token string) (uplevel.ChannelInfo, bool) {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(token), "profile_%d", &n); err != nil || n < 1 || n > len(r.channels) {
		if len(r.channels) > 0 {
			return r.channels[0], true // 未带 token 时默认第一通道
		}
		return uplevel.ChannelInfo{}, false
	}
	return r.channels[n-1], true
}

// resolveReq 解析路径 ID：优先按设备（一台相机多 profile），其次按通道（兼容）。
func (s *Server) resolveReq(id string) (svcReq, bool) {
	if dev, ok := s.up.Registry.Find(id); ok && len(dev.Channels) > 0 {
		return svcReq{id: id, deviceID: dev.DeviceID, channels: dev.Channels}, true
	}
	if _, ch, ok := s.up.Registry.FindByChannel(id); ok {
		return svcReq{id: id, deviceID: ch.DeviceID, channels: []uplevel.ChannelInfo{ch}}, true
	}
	return svcReq{}, false
}

// handleDeviceService 解析 SOAP 请求体，按 Body 首子元素分发到对应服务。
func (s *Server) handleDeviceService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("channel")
	req, ok := s.resolveReq(id)
	if !ok {
		writeSOAPFault(w, "unknown device or channel "+id)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeSOAPFault(w, "read body: "+err.Error())
		return
	}
	action, payload := parseSOAPAction(string(body))
	s.logger.Debug("onvif request", "id", id, "action", action, "channels", len(req.channels))

	var resp string
	switch action {
	case "GetSystemDateAndTime":
		resp = respGetSystemDateAndTime
	case "GetDeviceInformation":
		resp = s.respGetDeviceInformation(req)
	case "GetCapabilities":
		resp = s.respGetCapabilities(req)
	case "GetServices":
		resp = s.respGetServices(req)
	case "GetProfiles", "GetProfile":
		resp = s.respGetProfiles(req)
	case "GetStreamUri":
		// 模式决策：direct 模板可用且相机可达 → 直通地址（无需点播）；
		// 否则网关模式：自动发起 GB 点播（外部程序无需任何手动操作）。
		token := soapQuery(payload, "ProfileToken")
		ch, ok := req.channelByToken(token)
		if !ok {
			writeSOAPFault(w, "unknown profile token "+token)
			return
		}
		resp = s.streamURIResponse(req, ch)
	case "GetSnapshotUri":
		token := soapQuery(payload, "ProfileToken")
		if _, ok := req.channelByToken(token); !ok {
			writeSOAPFault(w, "unknown profile token "+token)
			return
		}
		resp = s.respGetSnapshotUri(req)
	case "GetVideoEncoderConfigurations", "GetVideoEncoderConfiguration",
		"GetVideoSourceConfigurations", "GetVideoSourceConfiguration",
		"GetVideoEncoderConfigurationOptions":
		resp = s.respVideoConfigs(req)
	case "ContinuousMove":
		resp = s.handleContinuousMove(req, payload)
	case "AbsoluteMove", "RelativeMove":
		// 国标 A50F 云台指令无绝对/相对定位，确认收到（不产生运动）。
		s.logger.Debug("onvif ptz absolute/relative move acknowledged (unsupported)", "id", id)
		resp = soapEnvelope(`<tptz:MoveResponse/>`)
	case "Stop":
		resp = s.handlePTZStop(req, payload)
	case "GetStatus":
		resp = respPTZStatus
	case "GetNodes":
		resp = s.respPTZNodes(req)
	case "GetConfigurations", "GetPTZConfigurations":
		resp = s.respPTZConfigurations(req)
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

// streamURIResponse 生成 GetStreamUri 应答（含直通/中转模式决策）。
func (s *Server) streamURIResponse(req svcReq, ch uplevel.ChannelInfo) string {
	gatewayURL := s.streamURL(ch.DeviceID)
	up := s.store.Root().Uplevel
	if _, dev, ok := s.channelInfo(ch.DeviceID); ok {
		if url, direct := uplevel.ResolveStreamURL(up, dev.RemoteIP, ch.DeviceID, gatewayURL); direct {
			s.logger.Info("onvif stream direct to camera", "channel", ch.DeviceID, "uri", url)
			return soapEnvelope(fmt.Sprintf(`
<trt:GetStreamUriResponse>
  <trt:MediaUri>
    <tt:Uri>%s</tt:Uri>
    <tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>
    <tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>
    <tt:TimeToLive>86400</tt:TimeToLive>
  </trt:MediaUri>
</trt:GetStreamUriResponse>`, xmlEscape(url)))
		}
	}
	// 网关模式：自动发起 GB 点播。
	go func() {
		ctx, cancel := contextTimeout(20 * time.Second)
		defer cancel()
		if _, err := s.up.Play(ctx, ch.DeviceID); err != nil {
			s.logger.Warn("onvif autoplay failed", "channel", ch.DeviceID, "err", err.Error())
		}
	}()
	return soapEnvelope(fmt.Sprintf(`
<trt:GetStreamUriResponse>
  <trt:MediaUri>
    <tt:Uri>%s</tt:Uri>
    <tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>
    <tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>
    <tt:TimeToLive>86400</tt:TimeToLive>
  </trt:MediaUri>
</trt:GetStreamUriResponse>`, xmlEscape(gatewayURL)))
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
