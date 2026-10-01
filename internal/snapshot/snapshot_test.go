package snapshot

import (
	"strings"
	"testing"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
)

// TestJPEGFromStreamFailsFast 不可达的 RTSP 地址应快速失败（连接拒绝或 ffmpeg 缺失）。
func TestJPEGFromStreamFailsFast(t *testing.T) {
	g := New("ffmpeg", nil)
	_, err := g.JPEGFromStream("cam1", "rtsp://127.0.0.1:1/cam/cam1")
	if err == nil {
		t.Fatal("want error for unreachable rtsp url")
	}
	if !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestJPEGDirectInvalidDevice 直连采集不可用设备应报错而非挂死。
func TestJPEGDirectInvalidDevice(t *testing.T) {
	g := New("ffmpeg", nil)
	_, err := g.JPEG(config.Camera{ID: "camx", Type: config.TypeV4L2, Source: "/dev/nonexistent"})
	if err == nil {
		t.Fatal("want error for invalid device")
	}
}
