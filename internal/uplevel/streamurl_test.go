package uplevel

import (
	"testing"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
)

// TestChannelSeq 通道序号提取。
func TestChannelSeq(t *testing.T) {
	cases := map[string]int{
		"34020000001320000001": 1,
		"34020000001320000002": 2,
		"34020000001320000420": 420,
		"short":                0,
	}
	for id, want := range cases {
		if got := ChannelSeq(id); got != want {
			t.Errorf("ChannelSeq(%s) = %d, want %d", id, got, want)
		}
	}
}

// TestCameraRTSPURL 模板渲染与模式判定。
func TestCameraRTSPURL(t *testing.T) {
	up := config.Uplevel{
		StreamMode:         config.StreamModeDirect,
		CameraRTSPTemplate: "rtsp://admin:pass@{ip}:554/Streaming/Channels/{n}01",
	}
	url, ok := CameraRTSPURL(up, "192.168.19.44", "34020000001320000001")
	if !ok {
		t.Fatal("direct mode with template should render URL")
	}
	want := "rtsp://admin:pass@192.168.19.44:554/Streaming/Channels/101"
	if url != want {
		t.Fatalf("url = %s, want %s", url, want)
	}
	// gateway 模式不渲染
	up.StreamMode = config.StreamModeGateway
	if _, ok := CameraRTSPURL(up, "192.168.19.44", "34020000001320000001"); ok {
		t.Fatal("gateway mode should not render camera URL")
	}
	// 空模板不渲染
	up.StreamMode = config.StreamModeDirect
	up.CameraRTSPTemplate = ""
	if _, ok := CameraRTSPURL(up, "192.168.19.44", "34020000001320000001"); ok {
		t.Fatal("empty template should not render camera URL")
	}
}

// TestResolveStreamURLFallback 相机不可达时回退网关地址。
func TestResolveStreamURLFallback(t *testing.T) {
	up := config.Uplevel{
		StreamMode:         config.StreamModeDirect,
		CameraRTSPTemplate: "rtsp://admin:pass@{ip}:554/Streaming/Channels/{n}01",
	}
	// 192.0.2.1（TEST-NET）必然不可达。
	url, direct := ResolveStreamURL(up, "192.0.2.1", "34020000001320000001", "rtsp://adapter:8556/gb/x")
	if direct {
		t.Fatal("unreachable camera should fall back to gateway")
	}
	if url != "rtsp://adapter:8556/gb/x" {
		t.Fatalf("gateway url = %s", url)
	}
}
