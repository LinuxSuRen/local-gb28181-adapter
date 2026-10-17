// 国标上级平台（下级设备接入）管理 API。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
	"github.com/linuxsuren/local-gb28181-adapter/internal/uplevel"
)

// uplevel 查询与点播的超时。
const (
	uplevelQueryTimeout  = 8 * time.Second
	uplevelInviteTimeout = 12 * time.Second
)

// ---- 配置 ----

func (s *Server) handleGetUplevel(w http.ResponseWriter, _ *http.Request) {
	root := s.store.Root()
	u := root.Uplevel
	writeData(w, http.StatusOK, map[string]any{
		"enabled":        u.Enabled,
		"server_id":      u.ServerID,
		"realm":          u.Realm(),
		"local_addr":     u.LocalAddr,
		"media_port_min": u.MediaPortMin,
		"media_port_max": u.MediaPortMax,
		"password_set":   u.Password != "",
		"stream_mode":    u.EffectiveStreamMode(),
		"camera_rtsp_template": u.CameraRTSPTemplate,
		"running":        s.uplevel.Running(),
		"listen_port":    s.uplevel.LocalPort(),
	})
}

type uplevelInput struct {
	Enabled      *bool   `json:"enabled"`
	ServerID     *string `json:"server_id"`
	Password     *string `json:"password"`
	LocalAddr    *string `json:"local_addr"`
	MediaPortMin *int    `json:"media_port_min"`
	MediaPortMax *int    `json:"media_port_max"`
	StreamMode         *string `json:"stream_mode"`
	CameraRTSPTemplate *string `json:"camera_rtsp_template"`
}

func (in *uplevelInput) validate() error {
	if in.ServerID != nil && *in.ServerID != "" && !gbIDValid(*in.ServerID) {
		return badRequest("平台编码必须为 20 位数字")
	}
	if in.LocalAddr != nil && *in.LocalAddr != "" {
		if _, _, err := splitHostPort(*in.LocalAddr); err != nil {
			return badRequest("SIP 监听地址格式应为 host:port 或 :5060")
		}
	}
	if in.MediaPortMin != nil && (*in.MediaPortMin < 1024 || *in.MediaPortMin > 65000) {
		return badRequest("收流端口下限取值范围 1024-65000")
	}
	if in.MediaPortMax != nil && (*in.MediaPortMax < 1024 || *in.MediaPortMax > 65000) {
		return badRequest("收流端口上限取值范围 1024-65000")
	}
	if in.MediaPortMin != nil && in.MediaPortMax != nil && *in.MediaPortMin > *in.MediaPortMax {
		return badRequest("收流端口下限不能大于上限")
	}
	if in.StreamMode != nil && *in.StreamMode != "" &&
		*in.StreamMode != config.StreamModeDirect && *in.StreamMode != config.StreamModeGateway {
		return badRequest("取流模式只支持 direct（相机直通）或 gateway（适配器中转）")
	}
	return nil
}

// applyUplevelInput 把 patch 输入应用到配置（空密码表示不变更）。
func applyUplevelInput(u *config.Uplevel, in *uplevelInput) error {
	if in.Enabled != nil {
		u.Enabled = *in.Enabled
	}
	if in.ServerID != nil && *in.ServerID != "" {
		u.ServerID = *in.ServerID
	}
	if in.Password != nil && *in.Password != "" {
		u.Password = *in.Password
	}
	if in.LocalAddr != nil && *in.LocalAddr != "" {
		u.LocalAddr = *in.LocalAddr
	}
	if in.MediaPortMin != nil {
		u.MediaPortMin = *in.MediaPortMin
	}
	if in.MediaPortMax != nil {
		u.MediaPortMax = *in.MediaPortMax
	}
	if in.StreamMode != nil {
		u.StreamMode = *in.StreamMode
	}
	if in.CameraRTSPTemplate != nil {
		u.CameraRTSPTemplate = strings.TrimSpace(*in.CameraRTSPTemplate)
	}
	return nil
}

func (s *Server) handlePutUplevel(w http.ResponseWriter, r *http.Request) {
	var in uplevelInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "请求体不是合法 JSON")
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	if err := s.store.Update(func(root *config.Root) error {
		return applyUplevelInput(&root.Uplevel, &in)
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "save_failed", "保存配置失败")
		return
	}
	if s.OnUplevelChange != nil {
		s.OnUplevelChange()
	}
	s.handleGetUplevel(w, r)
}

// ---- 设备与通道 ----

func (s *Server) handleUplevelDevices(w http.ResponseWriter, _ *http.Request) {
	devs := s.uplevel.Registry.Snapshot()
	up := s.store.Root().Uplevel
	// 附加红外标记（配置清单或名称关键词）。
	for i := range devs {
		infrared := false
		for _, ch := range devs[i].Channels {
			if up.IsInfraredChannel(ch.DeviceID, ch.Name) {
				infrared = true
			}
		}
		devs[i].Infrared = infrared
		for j := range devs[i].Channels {
			devs[i].Channels[j].Infrared = up.IsInfraredChannel(devs[i].Channels[j].DeviceID, devs[i].Channels[j].Name)
		}
	}
	writeData(w, http.StatusOK, devs)
}

// handleUplevelDeviceQuery 触发一次设备信息 + 目录查询并返回最新快照。
func (s *Server) handleUplevelDeviceQuery(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	if _, ok := s.uplevel.Registry.Find(deviceID); !ok {
		writeError(w, http.StatusNotFound, "not_found", "设备未注册")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), uplevelQueryTimeout)
	defer cancel()
	if _, err := s.uplevel.QueryDeviceInfo(ctx, deviceID); err != nil {
		s.logger.Warn("uplevel deviceinfo query failed", "device", deviceID, "err", err.Error())
	}
	if _, err := s.uplevel.QueryCatalog(ctx, deviceID); err != nil {
		writeError(w, http.StatusGatewayTimeout, "query_failed",
			"目录查询失败："+err.Error())
		return
	}
	dev, _ := s.uplevel.Registry.Find(deviceID)
	writeData(w, http.StatusOK, dev)
}

// handleUplevelInfrared 标记/取消通道的红外属性（持久化，影响 ONVIF
// profile 名称与发现 scope 的 infrared 标识）。
func (s *Server) handleUplevelInfrared(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("id")
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Enabled == nil {
		writeError(w, http.StatusBadRequest, "bad_json", "请求体需要 {\"enabled\": true|false}")
		return
	}
	if err := s.store.Update(func(root *config.Root) error {
		root.Uplevel.InfraredChannels = root.Uplevel.SetInfraredChannel(channelID, *in.Enabled)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "save_failed", "保存配置失败")
		return
	}
	writeData(w, http.StatusOK, map[string]bool{"infrared": *in.Enabled})
}

// ---- 点播会话 ----

func (s *Server) handleUplevelPlay(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("id")
	if r.Method == http.MethodDelete {
		if err := s.uplevel.StopPlay(channelID); err != nil {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeData(w, http.StatusOK, map[string]string{"stopped": channelID})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), uplevelInviteTimeout)
	defer cancel()
	info, err := s.uplevel.Play(ctx, channelID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "play_failed", err.Error())
		return
	}
	resp := map[string]any{
		"session":  info,
		"rtsp_url": s.gbStreamURL(channelID),
	}
	writeData(w, http.StatusOK, resp)
}

func (s *Server) handleUplevelSessions(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, s.uplevel.Sessions())
}

// ---- 云台 ----

type uplevelPTZInput struct {
	Action string `json:"action"`
	Speed  int    `json:"speed"`
}

func (s *Server) handleUplevelPTZ(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("id")
	var in uplevelPTZInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "请求体不是合法 JSON")
		return
	}
	if err := s.uplevel.SendPTZ(r.Context(), channelID, in.Action, in.Speed); err != nil {
		writeError(w, http.StatusBadGateway, "ptz_failed", err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]string{"ok": in.Action})
}

// ---- 快照 ----

// handleUplevelSnapshot 抓一帧 JPEG：
// direct 模式（模板可用且相机可达）优先从相机 RTSP 直连抓取，无需点播；
// 否则要求通道点播中，从内嵌 RTSP 抓取。
func (s *Server) handleUplevelSnapshot(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("id")
	up := s.store.Root().Uplevel
	// 直通优先。
	if dev, _, ok := lookupChannel(s.uplevel, channelID); ok {
		if url, direct := uplevel.ResolveStreamURL(up, dev.RemoteIP, channelID, ""); direct {
			s.writeStreamSnapshot(w, "gb-"+channelID, url)
			return
		}
	}
	playing := false
	for _, sess := range s.uplevel.Sessions() {
		if sess.ChannelID == channelID {
			playing = true
			break
		}
	}
	if !playing {
		writeError(w, http.StatusConflict, "not_playing", "请先点播该通道再抓图")
		return
	}
	if s.RTSPBaseURL == "" {
		writeError(w, http.StatusServiceUnavailable, "no_rtsp", "RTSP 服务未启用")
		return
	}
	s.writeStreamSnapshot(w, "gb-"+channelID, s.gbStreamURL(channelID))
}

// writeStreamSnapshot 从 RTSP 流抓帧并输出 JPEG。
func (s *Server) writeStreamSnapshot(w http.ResponseWriter, key, rtspURL string) {
	data, err := s.snaps.JPEGFromStream(key, rtspURL)
	if err != nil {
		s.logger.Warn("snapshot failed", "key", key, "url", rtspURL, "err", err.Error())
		writeError(w, http.StatusBadGateway, "snapshot_failed",
			"抓取画面失败："+err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// lookupChannel 在上级平台注册表中查找通道。
func lookupChannel(u *uplevel.Service, channelID string) (uplevel.Device, uplevel.ChannelInfo, bool) {
	return u.Registry.FindByChannel(channelID)
}

// gbStreamURL 通道对应的内嵌 RTSP 流地址。
func (s *Server) gbStreamURL(channelID string) string {
	return strings.TrimSuffix(s.RTSPBaseURL, "/") + "/gb/" + channelID
}
