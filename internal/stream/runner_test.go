package stream

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
}

func TestInputArgsV4L2(t *testing.T) {
	cam := config.Camera{Type: config.TypeV4L2, Source: "/dev/video0", Width: 1280, Height: 720, Framerate: 30}
	got := strings.Join(InputArgs(cam), " ")
	for _, want := range []string{"-f v4l2", "-framerate 30", "-video_size 1280x720", "-i /dev/video0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestInputArgsAVFoundation(t *testing.T) {
	cam := config.Camera{Type: config.TypeAVFoundation, Source: "1", Framerate: 30}
	got := strings.Join(InputArgs(cam), " ")
	if !strings.Contains(got, "-f avfoundation") || !strings.Contains(got, "-i 1") {
		t.Fatalf("avfoundation args: %q", got)
	}
	// 空源默认 0 号设备。
	got = strings.Join(InputArgs(config.Camera{Type: config.TypeAVFoundation}), " ")
	if !strings.Contains(got, "-i 0") {
		t.Fatalf("default avfoundation index: %q", got)
	}
}

func TestInputArgsRTSP(t *testing.T) {
	cam := config.Camera{Type: config.TypeRTSP, Source: "rtsp://127.0.0.1:8554/cam/cam1"}
	got := strings.Join(InputArgs(cam), " ")
	if !strings.Contains(got, "-rtsp_transport tcp") || !strings.Contains(got, "-i rtsp://127.0.0.1:8554/cam/cam1") {
		t.Fatalf("rtsp args: %q", got)
	}
}

func TestInputArgsTestSrc(t *testing.T) {
	cam := config.Camera{Type: config.TypeTestSrc, Width: 640, Height: 480, Framerate: 10}
	got := strings.Join(InputArgs(cam), " ")
	if !strings.Contains(got, "testsrc=size=640x480:rate=10") {
		t.Fatalf("testsrc args: %q", got)
	}
}

func TestBuildArgsOutput(t *testing.T) {
	cam := config.Camera{Type: config.TypeTestSrc, Framerate: 15, BitrateKBPS: 2048}
	got := strings.Join(BuildArgs(cam, "rtsp://127.0.0.1:8554/cam/cam1"), " ")
	for _, want := range []string{"libx264", "-f rtsp", "-rtsp_transport tcp", "rtsp://127.0.0.1:8554/cam/cam1", "-b:v 2048k", "-an"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestSpecSensitivity(t *testing.T) {
	base := config.Camera{Type: config.TypeV4L2, Source: "/dev/video0", Framerate: 15, BitrateKBPS: 2048}
	if Spec(base) != Spec(base) {
		t.Fatal("same camera should have same spec")
	}
	changed := base
	changed.Framerate = 25
	if Spec(base) == Spec(changed) {
		t.Fatal("framerate change should alter spec")
	}
	changed = base
	changed.Source = "/dev/video1"
	if Spec(base) == Spec(changed) {
		t.Fatal("source change should alter spec")
	}
}

func TestManagerSyncStartStop(t *testing.T) {
	m := NewManager("rtsp://127.0.0.1:8554", "false", testLogger())
	cam1 := config.Camera{ID: "cam1", Type: config.TypeTestSrc, Enabled: true}
	m.Sync([]config.Camera{cam1})
	if _, ok := m.Status("cam1"); !ok {
		t.Fatal("runner should exist")
	}
	// 禁用后应停止并移除。
	m.Sync([]config.Camera{{ID: "cam1", Type: config.TypeTestSrc}})
	if _, ok := m.Status("cam1"); ok {
		t.Fatal("runner should be removed")
	}
	// 参数变化（spec 不同）应能再次接受新 runner。
	m.Sync([]config.Camera{{ID: "cam1", Type: config.TypeTestSrc, Enabled: true, Framerate: 25}})
	if _, ok := m.Status("cam1"); !ok {
		t.Fatal("runner should be recreated")
	}
	m.StopAll()
}
