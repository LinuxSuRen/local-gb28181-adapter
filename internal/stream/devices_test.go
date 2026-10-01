package stream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
)

func TestListV4L2DevicesWithFakeTree(t *testing.T) {
	root := t.TempDir()
	// 构造：video0 = USB 主摄像头（index 0），video1 = 同一 USB 设备的元数据节点
	//（index 1，应被过滤），video2 = PCIe 内置摄像头（index 0）。
	makeNode := func(base, name, index, deviceLink string) {
		dir := filepath.Join(root, "sys", "class", "video4linux", base)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if name != "" {
			_ = os.WriteFile(filepath.Join(dir, "name"), []byte(name), 0o644)
		}
		if index != "" {
			_ = os.WriteFile(filepath.Join(dir, "index"), []byte(index), 0o644)
		}
		if deviceLink != "" {
			_ = os.Symlink(deviceLink, filepath.Join(dir, "device"))
		}
	}
	makeNode("video0", "USB2.0 HD UVC WebCam", "0",
		"../../../../devices/pci0000:00/0000:00:14.0/usb3/3-2/3-2:1.0/video4linux/video0")
	makeNode("video1", "USB2.0 HD UVC WebCam", "1",
		"../../../../devices/pci0000:00/0000:00:14.0/usb3/3-2/3-2:1.0/video4linux/video1")
	makeNode("video2", "Intel built-in Camera", "0",
		"../../../../devices/pci0000:00/0000:00:16.0/video4linux/video2")

	devDir := filepath.Join(root, "dev")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"video1", "video0", "video2", "video-control"} {
		_ = os.WriteFile(filepath.Join(devDir, n), nil, 0o666)
	}

	devices, err := listV4L2Devices(filepath.Join(root, "sys"), devDir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 元数据节点与非法命名节点被过滤，仅剩主采集节点，且按序号排序。
	if len(devices) != 2 {
		t.Fatalf("devices = %+v, want 2", devices)
	}
	if devices[0].Source != filepath.Join(devDir, "video0") || devices[0].Name != "USB2.0 HD UVC WebCam" {
		t.Fatalf("device0: %+v", devices[0])
	}
	if devices[0].Type != string(config.TypeV4L2) || devices[0].Kind != kindUSB {
		t.Fatalf("device0 meta: %+v", devices[0])
	}
	if devices[1].Kind != kindBuiltin {
		t.Fatalf("device2 should be builtin: %+v", devices[1])
	}
}

func TestParseAVFDevices(t *testing.T) {
	sample := `[avfoundation @ 0x7fa] AVFoundation video devices:
[avfoundation @ 0x7fa] [0] FaceTime高清相机（内建）
[avfoundation @ 0x7fa] [1] Capture screen 0
[avfoundation @ 0x7fa] [2] USB2.0 HD UVC WebCam
[avfoundation @ 0x7fa] AVFoundation audio devices:
[avfoundation @ 0x7fa] [0] MacBook Pro Microphone
`
	devices := parseAVFDevices(sample)
	// 屏幕采集设备被过滤。
	if len(devices) != 2 {
		t.Fatalf("devices = %+v, want 2 video devices", devices)
	}
	if devices[0].Name != "FaceTime高清相机（内建）" || devices[0].Source != "0" {
		t.Fatalf("device0: %+v", devices[0])
	}
	if devices[0].Kind != kindBuiltin || devices[1].Kind != kindUSB {
		t.Fatalf("kinds: %+v", devices)
	}
	if devices[0].Type != string(config.TypeAVFoundation) {
		t.Fatalf("type: %+v", devices[0])
	}
}

func TestParseDShowDevices(t *testing.T) {
	sample := `[dshow @ 0000026c4e05a940]  DirectShow video devices (some may be both video and audio devices)
[dshow @ 0000026c4e05a940]  "Integrated Camera"
[dshow @ 0000026c4e05a940]     Alternative name "@device_cm_{33D9A762-90C8-11D0-BD43-00A0C911CE86}\Integrated Camera"
[dshow @ 0000026c4e05a940]  "USB2.0 HD UVC WebCam"
[dshow @ 0000026c4e05a940]  DirectShow audio devices
[dshow @ 0000026c4e05a940]  "Microphone (Realtek Audio)"
`
	devices := parseDShowDevices(sample)
	if len(devices) != 2 {
		t.Fatalf("devices = %+v, want 2", devices)
	}
	if devices[0].Name != "Integrated Camera" || devices[0].Source != "Integrated Camera" {
		t.Fatalf("device0: %+v", devices[0])
	}
	if devices[0].Kind != kindBuiltin || devices[1].Kind != kindUSB {
		t.Fatalf("kinds: %+v", devices)
	}
	if devices[0].Type != string(config.TypeDShow) {
		t.Fatalf("type: %+v", devices[0])
	}
}

// TestParseDShowDevicesNewFormat 使用 FFmpeg 7.1+（统一设备列表输出）的真实输出作为 fixture。
func TestParseDShowDevicesNewFormat(t *testing.T) {
	sample := `[in#0 @ 000001d24d600580] "WL24A" (video)
[in#0 @ 000001d24d600580]   Alternative name "@device_pnp_\\?\usb#vid_2f9d&pid_0024&mi_00#6&19fd0de&0&0000#{65e8773d-8f56-11d0-a3b9-00a0c9223196}\global"
[in#0 @ 000001d24d600580] "麦克风 (WL24A)" (audio)
[in#0 @ 000001d24d600580]   Alternative name "@device_cm_{33D9A762-90C8-11D0-BD43-00A0C911CE86}\wave_{...}"
[in#0 @ 000001d24d600580] "Camera (Front)" (video, audio)
[in#0 @ 000001d24d600580]   Alternative name "@device_pnp_\\?\pci#ven_8086&dev_9a32..."
`
	devices := parseDShowDevices(sample)
	if len(devices) != 2 {
		t.Fatalf("devices = %+v, want 2 (video 与 video,audio；audio 跳过)", devices)
	}
	if devices[0].Name != "WL24A" || devices[0].Source != "WL24A" {
		t.Fatalf("device0: %+v", devices[0])
	}
	// PnP 路径含 usb# → 标记为 USB（比名字猜测准确）。
	if devices[0].Kind != kindUSB {
		t.Fatalf("WL24A should be usb via pnp path: %+v", devices[0])
	}
	// 音视频合并设备保留；名字含括号不误判媒体类型；pci 路径不标 usb。
	if devices[1].Name != "Camera (Front)" || devices[1].Kind == kindUSB {
		t.Fatalf("device1: %+v", devices[1])
	}
	if devices[0].Type != string(config.TypeDShow) {
		t.Fatalf("type: %+v", devices[0])
	}
}

// TestParseDShowDevicesOldFormatParenName 旧格式下设备名含括号不应被当成媒体类型。
func TestParseDShowDevicesOldFormatParenName(t *testing.T) {
	sample := `[dshow @ ...]  DirectShow video devices (some may be both video and audio devices)
[dshow @ ...]  "Camera (Front)"
[dshow @ ...]     Alternative name "@device_cm_{...}"
`
	devices := parseDShowDevices(sample)
	if len(devices) != 1 || devices[0].Name != "Camera (Front)" {
		t.Fatalf("devices: %+v", devices)
	}
}

// TestParseAVFDevicesNewFormat 新版 FFmpeg 的 avfoundation 输出在设备名后带 [uid:]/[serial:] 后缀。
func TestParseAVFDevicesNewFormat(t *testing.T) {
	sample := `[avfoundation @ 0x7fa] AVFoundation video devices:
[avfoundation @ 0x7fa] [0] FaceTime HD Camera [uid:0x1a11000005ac8500] [serial:XYZ]
[avfoundation @ 0x7fa] [1] USB2.0 HD UVC WebCam [uid:0x24a11000005ac8700]
[avfoundation @ 0x7fa] [2] Capture screen 0 [uid:screen]
[avfoundation @ 0x7fa] AVFoundation audio devices:
[avfoundation @ 0x7fa] [0] MacBook Pro Microphone [uid:Built-in Microphone]
`
	devices := parseAVFDevices(sample)
	if len(devices) != 2 {
		t.Fatalf("devices = %+v, want 2", devices)
	}
	if devices[0].Name != "FaceTime HD Camera" {
		t.Fatalf("uid/serial suffix not stripped: %+v", devices[0])
	}
	if devices[1].Name != "USB2.0 HD UVC WebCam" {
		t.Fatalf("uid suffix not stripped: %+v", devices[1])
	}
	if devices[0].Kind != kindBuiltin || devices[1].Kind != kindUSB {
		t.Fatalf("kinds: %+v", devices)
	}
}

func TestGuessKind(t *testing.T) {
	cases := map[string]string{
		"FaceTime HD Camera":   kindBuiltin,
		"Integrated Camera":    kindBuiltin,
		"内置摄像头":                kindBuiltin,
		"USB2.0 HD UVC WebCam": kindUSB,
		"Some Generic Camera":  kindUnknown,
	}
	for name, want := range cases {
		if got := guessKind(name); got != want {
			t.Fatalf("guessKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDefaultLocalType(t *testing.T) {
	if DefaultLocalType() == "" {
		t.Fatal("default local type should be non-empty on supported platforms")
	}
}

func TestInputArgsDShow(t *testing.T) {
	cam := config.Camera{Type: config.TypeDShow, Source: "Integrated Camera", Width: 1280, Height: 720, Framerate: 30}
	got := strings.Join(InputArgs(cam), " ")
	for _, want := range []string{"-f dshow", "-framerate 30", "-video_size 1280x720", `-i video=Integrated Camera`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	// 已带 video= 前缀时不重复包装。
	cam.Source = `video=Integrated Camera`
	got = strings.Join(InputArgs(cam), " ")
	if strings.Count(got, "video=") != 1 {
		t.Fatalf("double video= prefix: %q", got)
	}
	// 别名形式原样使用。
	cam.Source = `video=@device_cm_{GUID}\Cam`
	got = strings.Join(InputArgs(cam), " ")
	if !strings.Contains(got, `-i video=@device_cm_{GUID}\Cam`) {
		t.Fatalf("alias passthrough: %q", got)
	}
}
