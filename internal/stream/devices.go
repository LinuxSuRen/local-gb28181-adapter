package stream

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
)

// ErrUnsupportedPlatform 当前平台不支持本地设备枚举。
var ErrUnsupportedPlatform = fmt.Errorf("unsupported platform for device enumeration")

// Device 本地视频设备的跨平台统一抽象。
// 用户在 UI 里只看到"有哪些摄像头"；平台差异由 Type 字段消化。
type Device struct {
	Name   string `json:"name"`   // 展示名（内置摄像头名、USB 摄像头名等）
	Source string `json:"source"` // ffmpeg 输入源：v4l2 设备路径 / avfoundation 索引 / dshow 设备名
	Type   string `json:"type"`   // 摄像头类型：v4l2 | avfoundation | dshow
	Kind   string `json:"kind"`   // builtin | usb | unknown（尽力识别，仅用于展示）
}

const (
	kindBuiltin = "builtin"
	kindUSB     = "usb"
	kindUnknown = "unknown"
	enumTimeout = 10 * time.Second
)

// ListLocalVideoDevices 枚举当前平台的本机视频设备（内置/USB 摄像头）。
// 找不到设备时返回空列表而非错误；错误仅在枚举机制本身失败时返回。
func ListLocalVideoDevices(ffmpegBin string) ([]Device, error) {
	switch runtime.GOOS {
	case "linux":
		return listV4L2Devices("/sys", "/dev")
	case "darwin":
		return listAVFDevices(ffmpegBin)
	case "windows":
		return listDShowDevices(ffmpegBin)
	default:
		return nil, ErrUnsupportedPlatform
	}
}

// DefaultLocalType 返回当前平台的本地摄像头类型（供 UI 手动输入兜底）。
func DefaultLocalType() string {
	switch runtime.GOOS {
	case "linux":
		return string(config.TypeV4L2)
	case "darwin":
		return string(config.TypeAVFoundation)
	case "windows":
		return string(config.TypeDShow)
	default:
		return ""
	}
}

// ---- Linux / V4L2 ----

// listV4L2Devices 扫描 /dev/video* 并从 sysfs 读取设备名与连接方式。
// sysRoot/devRoot 可注入，便于测试。
func listV4L2Devices(sysRoot, devRoot string) ([]Device, error) {
	type info struct{ name, index, kind string }
	sysInfo := map[string]info{}
	if entries, err := os.ReadDir(filepath.Join(sysRoot, "class", "video4linux")); err == nil {
		for _, e := range entries {
			base := e.Name()
			if !strings.HasPrefix(base, "video") {
				continue
			}
			readFile := func(name string) string {
				b, err := os.ReadFile(filepath.Join(sysRoot, "class", "video4linux", base, name))
				if err != nil {
					return ""
				}
				return strings.TrimSpace(string(b))
			}
			it := info{name: readFile("name"), index: readFile("index")}
			// 连接方式：device 符号链接指向 usb 视为 USB，pci 视为内置。
			if link, err := os.Readlink(filepath.Join(sysRoot, "class", "video4linux", base, "device")); err == nil {
				switch {
				case strings.Contains(link, "/usb"):
					it.kind = kindUSB
				case strings.Contains(link, "/pci"):
					it.kind = kindBuiltin
				default:
					it.kind = kindUnknown
				}
			} else {
				it.kind = kindUnknown
			}
			sysInfo[base] = it
		}
	}
	nodes, err := filepath.Glob(filepath.Join(devRoot, "video*"))
	if err != nil {
		return nil, fmt.Errorf("glob video devices: %w", err)
	}
	sort.Slice(nodes, func(i, j int) bool {
		mi, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(nodes[i]), "video"))
		mj, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(nodes[j]), "video"))
		return mi < mj
	})
	var devices []Device
	for _, node := range nodes {
		base := filepath.Base(node)
		// 只接受形如 video0 的采集节点（跳过 video-reactive 之类的无关设备）。
		if _, err := strconv.Atoi(strings.TrimPrefix(base, "video")); err != nil {
			continue
		}
		it := sysInfo[base]
		// UVC 摄像头会注册多个节点（采集 + 元数据），只保留 index 0 的主节点。
		if it.index != "" && it.index != "0" {
			continue
		}
		name := it.name
		if name == "" {
			name = base
		}
		devices = append(devices, Device{
			Name:   name,
			Source: node,
			Type:   string(config.TypeV4L2),
			Kind:   it.kind,
		})
	}
	return devices, nil
}

// ---- macOS / AVFoundation ----

// avfDeviceRe 匹配设备行中的 "[N] 设备名"（行首带 [avfoundation @ 0x..] 日志前缀）。
var avfDeviceRe = regexp.MustCompile(`\[(\d+)\]\s+(.+)$`)

// listAVFDevices 通过 `ffmpeg -f avfoundation -list_devices true -i ""` 枚举。
func listAVFDevices(ffmpegBin string) ([]Device, error) {
	stderr, err := runListCommand(ffmpegBin, "-f", "avfoundation", "-list_devices", "true", "-i", "")
	if err != nil {
		return nil, err
	}
	return parseAVFDevices(stderr), nil
}

// parseAVFDevices 解析 avfoundation 设备列表的 stderr 输出。
func parseAVFDevices(stderr string) []Device {
	var devices []Device
	section := ""
	for _, line := range strings.Split(stderr, "\n") {
		switch {
		case strings.Contains(line, "AVFoundation video devices"):
			section = "video"
			continue
		case strings.Contains(line, "AVFoundation audio devices"):
			section = "audio"
			continue
		}
		if section != "video" {
			continue
		}
		if m := avfDeviceRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			name := m[2]
			// 新版 FFmpeg 在设备名后追加了 [uid:...] [serial:...] 信息，剥离。
			if idx := strings.Index(name, " [uid:"); idx >= 0 {
				name = strings.TrimSpace(name[:idx])
			}
			// 屏幕采集（Capture screen N）不是摄像头，过滤。
			if strings.HasPrefix(name, "Capture screen") {
				continue
			}
			devices = append(devices, Device{
				Name:   name,
				Source: m[1],
				Type:   string(config.TypeAVFoundation),
				Kind:   guessKind(name),
			})
		}
	}
	return devices
}

// ---- Windows / DirectShow ----

// quotedNameRe 匹配 dshow 设备列表中带引号的设备名行。
var quotedNameRe = regexp.MustCompile(`"(.+)"`)

// listDShowDevices 通过 `ffmpeg -list_devices true -f dshow -i dummy` 枚举。
func listDShowDevices(ffmpegBin string) ([]Device, error) {
	stderr, err := runListCommand(ffmpegBin, "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	if err != nil {
		return nil, err
	}
	return parseDShowDevices(stderr), nil
}

// dshowMediaTypeRe 匹配新格式（FFmpeg 7.1+ 统一设备列表）行尾的媒体类型括号组，
// 如 (video)、(video, audio)、(audio)、(none)。限定小写媒体类型词，
// 避免把设备名中普通的括号后缀（如 "Camera (Front)"）误判为媒体类型。
var dshowMediaTypeRe = regexp.MustCompile(`\((?:video|audio|none)(?:, (?:video|audio|none))*\)$`)

// parseDShowDevices 解析 dshow 设备列表的 stderr 输出，兼容两种格式：
//   - 旧版（约 ≤7.0）：以 "DirectShow video devices" / "DirectShow audio devices"
//     分节，设备行形如 `"Integrated Camera"`（无媒体类型后缀）。
//   - 新版（7.1+ 统一设备列表）：无分节标题，每个设备一行，形如
//     `"WL24A" (video)`（纯音频为 `(audio)`，音视频合并为 `(video, audio)`），
//     每个设备后跟一行 `Alternative name "..."`（含 PnP 路径，可识别 USB 连接）。
func parseDShowDevices(stderr string) []Device {
	var devices []Device
	section := ""
	pending := -1 // 紧邻上一行的视频设备在 devices 中的下标（用于回填 Alternative name 信息）
	for _, line := range strings.Split(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		// Alternative name 行两种格式都有且含引号，先处理再跳过，防止被设备名正则误匹配。
		if strings.Contains(line, "Alternative name") {
			if pending >= 0 && strings.Contains(trimmed, "usb#") {
				devices[pending].Kind = kindUSB
			}
			pending = -1
			continue
		}
		switch {
		case strings.Contains(line, "DirectShow video devices"):
			section = "video"
			pending = -1
			continue
		case strings.Contains(line, "DirectShow audio devices"):
			section = "audio"
			pending = -1
			continue
		}
		m := quotedNameRe.FindStringSubmatch(line)
		if m == nil {
			pending = -1
			continue
		}
		keep := false
		if media := dshowMediaTypeRe.FindString(trimmed); media != "" {
			// 新格式：按行尾媒体类型判断，纯音频/无媒体跳过。
			keep = strings.Contains(media, "video")
		} else {
			// 旧格式：依赖分节标题。
			keep = section == "video"
		}
		if !keep {
			pending = -1
			continue
		}
		devices = append(devices, Device{
			Name:   m[1],
			Source: m[1],
			Type:   string(config.TypeDShow),
			Kind:   guessKind(m[1]),
		})
		pending = len(devices) - 1
	}
	return devices
}

// runListCommand 执行 ffmpeg 设备列表命令；列表输出在 stderr 且退出码非 0，属预期。
func runListCommand(ffmpegBin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), enumTimeout)
	defer cancel()
	//nolint:gosec // 二进制路径来自受控配置
	cmd := exec.CommandContext(ctx, ffmpegBin, append([]string{"-hide_banner", "-loglevel", "info"}, args...)...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	_ = cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("enumerate devices timeout")
	}
	if errBuf.Len() == 0 && out.Len() == 0 {
		return "", fmt.Errorf("ffmpeg produced no output")
	}
	return errBuf.String(), nil
}

// guessKind 依据设备名猜测内置/USB（best effort，仅展示）。
func guessKind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "builtin") || strings.Contains(n, "built-in") ||
		strings.Contains(n, "内置") || strings.Contains(n, "facetime") ||
		strings.Contains(n, "integrated"):
		return kindBuiltin
	case strings.Contains(n, "usb") || strings.Contains(n, "uvc") || strings.Contains(n, "外接"):
		return kindUSB
	default:
		return kindUnknown
	}
}
