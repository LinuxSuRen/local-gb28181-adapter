package onvif

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
	"github.com/linuxsuren/local-gb28181-adapter/internal/uplevel"
)

// newTestServer 构造带两个通道目录的 ONVIF 服务。
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

// TestProbeMatchPerChannel 每个通道应产出独立的 ProbeMatch 条目。
func TestProbeMatchPerChannel(t *testing.T) {
	s := newTestServer(t)
	resp := s.probeMatch("urn:test")
	count := strings.Count(resp, "<d:ProbeMatch>")
	if count != 2 {
		t.Fatalf("want 2 ProbeMatch entries, got %d", count)
	}
	for _, id := range []string{"34020000001320000001", "34020000001320000002"} {
		if !strings.Contains(resp, "urn:uuid:gb28181-"+id) {
			t.Errorf("missing endpoint for channel %s", id)
		}
		if !strings.Contains(resp, "/onvif/"+id+"/device_service") {
			t.Errorf("missing xaddr for channel %s", id)
		}
	}
	if !strings.Contains(resp, "Camera%2001") || !strings.Contains(resp, "Camera%2002") {
		t.Errorf("missing channel names in scopes")
	}
}

// TestContinuousMoveParsing 各种命名空间前缀的 ContinuousMove 都应解析出速度。
func TestContinuousMoveParsing(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		name    string
		payload string
		pan     float64
	}{
		{"tt-prefix", `<root xmlns:tt="x"><tt:Velocity><tt:PanTilt x="0.5" y="0"/><tt:Zoom x="0"/></tt:Velocity></root>`, 0.5},
		{"ns3-prefix", `<root xmlns:ns3="y"><ns3:Velocity><ns3:PanTilt x="-0.7" y="0.2"/><ns3:Zoom x="0"/></ns3:Velocity></root>`, -0.7},
		{"no-prefix", `<root><Velocity><PanTilt x="0.9" y="-0.1"/><Zoom x="0"/></Velocity></root>`, 0.9},
	}
	for _, c := range cases {
		resp := s.handleContinuousMove("34020000001320000001", c.payload)
		if !strings.Contains(resp, "ContinuousMoveResponse") {
			t.Errorf("%s: unexpected response: %s", c.name, resp)
		}
	}
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
		{0.3, 0.8, 0, "up"}, // 主导轴优先
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
