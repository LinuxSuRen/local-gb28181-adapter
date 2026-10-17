package onvif

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
	"github.com/linuxsuren/local-gb28181-adapter/internal/uplevel"
)

// newTestServer 构造带双通道（可见光+热成像）目录的 ONVIF 服务。
func newTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := config.LoadStore(t.TempDir())
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	up := uplevel.New(store, nil, slog.Default())
	up.Registry.Register("34020000001320000001", "192.168.19.44", 5060, 3600)
	up.Registry.UpdateChannels("34020000001320000001", []uplevel.ChannelInfo{
		{DeviceID: "34020000001320000001", Name: "Camera 01", Status: "ON"},
		{DeviceID: "34020000001320000002", Name: "Camera 02", Status: "ON"},
	})
	s := New(store, up, slog.Default())
	s.SetEndpoints("http://192.168.18.165:18080", 8556)
	return s
}

// TestProbeMatchPerDevice 每台物理设备一个发现条目（多通道聚合）。
func TestProbeMatchPerDevice(t *testing.T) {
	s := newTestServer(t)
	resp := s.probeMatch("urn:test")
	if count := strings.Count(resp, "<d:ProbeMatch>"); count != 1 {
		t.Fatalf("want 1 ProbeMatch (per device), got %d", count)
	}
	if !strings.Contains(resp, "urn:uuid:gb28181-34020000001320000001") {
		t.Fatal("missing device endpoint")
	}
	if !strings.Contains(resp, "/onvif/34020000001320000001/device_service") {
		t.Fatal("missing device service xaddr")
	}
}

// TestResolveReqDeviceAndChannel 路径 ID 解析：设备优先，通道兜底。
func TestResolveReqDeviceAndChannel(t *testing.T) {
	s := newTestServer(t)
	// 设备编码 → 两通道。
	req, ok := s.resolveReq("34020000001320000001")
	if !ok || len(req.channels) != 2 {
		t.Fatalf("device resolve: ok=%v channels=%d", ok, len(req.channels))
	}
	// 通道编码（兼容模式）→ 单通道。
	req2, ok := s.resolveReq("34020000001320000002")
	if !ok || len(req2.channels) != 1 {
		t.Fatalf("channel resolve: ok=%v channels=%d", ok, len(req2.channels))
	}
}

// TestRespGetProfilesMultiChannel 一台相机应返回多 profile，红外带后缀。
func TestRespGetProfilesMultiChannel(t *testing.T) {
	s := newTestServer(t)
	// 标记第二通道红外。
	if err := s.store.Update(func(root *config.Root) error {
		root.Uplevel.InfraredChannels = []string{"34020000001320000002"}
		return nil
	}); err != nil {
		t.Fatalf("mark infrared: %v", err)
	}
	req, _ := s.resolveReq("34020000001320000001")
	resp := s.respGetProfiles(req)
	if n := strings.Count(resp, "<trt:Profiles token="); n != 2 {
		t.Fatalf("want 2 profiles, got %d", n)
	}
	if !strings.Contains(resp, `token="profile_1"`) || !strings.Contains(resp, `token="profile_2"`) {
		t.Fatal("missing profile tokens profile_1/profile_2")
	}
	if !strings.Contains(resp, "_infrared") {
		t.Fatal("infrared profile name missing")
	}
}

// TestChannelByToken token 映射通道。
func TestChannelByToken(t *testing.T) {
	s := newTestServer(t)
	req, _ := s.resolveReq("34020000001320000001")
	if ch, ok := req.channelByToken("profile_1"); !ok || ch.DeviceID != "34020000001320000001" {
		t.Fatalf("profile_1 => %+v", ch)
	}
	if ch, ok := req.channelByToken("profile_2"); !ok || ch.DeviceID != "34020000001320000002" {
		t.Fatalf("profile_2 => %+v", ch)
	}
	// 无 token 默认第一通道。
	if ch, ok := req.channelByToken(""); !ok || ch.DeviceID != "34020000001320000001" {
		t.Fatalf("empty token should default to first channel, got %+v", ch)
	}
}

// TestContinuousMoveParsing 各种命名空间前缀的 ContinuousMove 都应解析出速度。
func TestContinuousMoveParsing(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		name    string
		payload string
	}{
		{"tt-prefix", `<root xmlns:tt="x"><tt:Velocity><tt:PanTilt x="0.5" y="0"/><tt:Zoom x="0"/></tt:Velocity></root>`},
		{"ns3-prefix", `<root xmlns:ns3="y"><ns3:Velocity><ns3:PanTilt x="-0.7" y="0.2"/><ns3:Zoom x="0"/></ns3:Velocity></root>`},
		{"no-prefix", `<root><Velocity><PanTilt x="0.9" y="-0.1"/><Zoom x="0"/></Velocity></root>`},
	}
	for _, c := range cases {
		resp := s.handleContinuousMove(req0(s), c.payload)
		if !strings.Contains(resp, "ContinuousMoveResponse") {
			t.Errorf("%s: unexpected response: %s", c.name, resp)
		}
	}
}

// req0 构造请求上下文。
func req0(s *Server) svcReq {
	req, _ := s.resolveReq("34020000001320000001")
	return req
}

// TestMapVelocity 速度方向映射。
func TestMapVelocity(t *testing.T) {
	cases := []struct {
		pan, tilt, zoom float64
		action          string
	}{
		{0.5, 0, 0, "right"},
		{-0.5, 0, 0, "left"},
		{0, 0.5, 0, "up"},
		{0, -0.5, 0, "down"},
		{0, 0, 0.5, "zoom_in"},
		{0, 0, -0.5, "zoom_out"},
		{0.3, 0.8, 0, "up"},
		{0, 0, 0, ""},
	}
	for _, c := range cases {
		action, _ := mapVelocity(c.pan, c.tilt, c.zoom)
		if action != c.action {
			t.Errorf("mapVelocity(%.2f,%.2f,%.2f) = %q, want %q", c.pan, c.tilt, c.zoom, action, c.action)
		}
	}
}

// TestStreamURUsesRTSPPort StreamUri 必须指向 RTSP 端口而非 HTTP 端口。
func TestStreamURUsesRTSPPort(t *testing.T) {
	s := newTestServer(t)
	uri := s.streamURL("34020000001320000001")
	want := "rtsp://192.168.18.165:8556/gb/34020000001320000001"
	if uri != want {
		t.Fatalf("stream uri = %s, want %s", uri, want)
	}
}
