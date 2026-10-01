// Package gbcode 提供国标（GB/T 28181）设备编码的生成校验与 PTZ 控制指令解析。
package gbcode

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 20 位国标编码结构：8 位行政区码 + 2 位行业码 + 3 位类型码 + 7 位序号。
// 常用类型码：200 = 中心/平台设备，132 = 网络摄像机通道。
const (
	CodeLen        = 20
	TypeCenter     = "200"
	TypeIPC        = "132"
	IndustryCode   = "00"
	DefaultPrefix  = "34020000" // 默认行政区码
)

// ErrInvalidID 编码不是 20 位数字。
var ErrInvalidID = errors.New("invalid gb28181 id: want 20 digits")

// Valid 校验 20 位国标编码格式。
func Valid(id string) bool {
	if len(id) != CodeLen {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Generate 生成 20 位编码：prefix 为 8 位行政区码（缺省补默认），
// typeCode 为 3 位类型码，seq 为 7 位以内序号。
func Generate(prefix, typeCode string, seq int) string {
	if len(prefix) < 8 {
		prefix = prefix + strings.Repeat("0", 8-len(prefix))
	}
	prefix = prefix[:8]
	if seq < 0 {
		seq = 0
	}
	if seq > 9999999 {
		seq = seq % 10000000
	}
	return prefix + IndustryCode + typeCode + fmt.Sprintf("%07d", seq)
}

// ChannelID 为摄像头生成国标通道编码。prefix 取设备编码前 8 位，
// seq 使用摄像头数字 ID（配置保证其唯一且稳定）。
func ChannelID(deviceID string, cameraSeq int) string {
	prefix := DefaultPrefix
	if len(deviceID) >= 8 {
		prefix = deviceID[:8]
	}
	if cameraSeq < 0 {
		cameraSeq = 0
	}
	return Generate(prefix, TypeIPC, cameraSeq)
}

// Domain 返回编码的归属域（前 10 位，国标 SIP 域编码）。
func Domain(id string) string {
	if len(id) < 10 {
		return id
	}
	return id[:10]
}

// ---- PTZ 控制指令（A50F 格式）----
//
// 帧格式（8 字节，hex 字符串 16 位）：
//
//	A5 0F 01 cmdCode 水平速度 垂直速度 组合码 校验码
//	校验码 = 前 7 字节之和 mod 256
//
// cmdCode 位布局（与 WVP-GB28181-pro 前端控制码一致）：
//
//	bit0 右转  bit1 左转  bit2 下仰  bit3 上仰  bit4 变倍放大  bit5 变倍缩小
type PTZ struct {
	Pan  int // -1 左 / 0 停 / 1 右
	Tilt int // -1 下 / 0 停 / 1 上
	Zoom int // -1 缩小 / 0 停 / 1 放大

	PanSpeed  int // 0~255，0 表示默认
	TiltSpeed int // 0~255
	ZoomSpeed int // 0~15（组合码高 4 位）
}

// 错误定义。
var (
	ErrBadPTZCmd     = errors.New("invalid ptz command frame")
	ErrPTZChecksum   = errors.New("ptz command checksum mismatch")
	ErrUnsupported   = errors.New("unsupported ptz command code")
	ErrPresetSupport = errors.New("preset commands not supported")
)

// ParsePTZCmd 解析 DeviceControl PTZCmd 字段的 A50F 指令串。
// 返回 PTZ 动作语义；非方向/变倍类指令（预置位、巡航等，cmdCode >= 0x40）
// 返回 ErrUnsupported，调用方按 no-op 处理。
func ParsePTZCmd(hexCmd string) (PTZ, error) {
	var ptz PTZ
	if len(hexCmd) < 16 {
		return ptz, fmt.Errorf("%w: want 16 hex chars, got %d", ErrBadPTZCmd, len(hexCmd))
	}
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		v, err := strconv.ParseUint(hexCmd[i*2:i*2+2], 16, 8)
		if err != nil {
			return ptz, fmt.Errorf("%w: %s", ErrBadPTZCmd, err.Error())
		}
		b[i] = byte(v)
	}
	if b[0] != 0xA5 || b[1] != 0x0F {
		return ptz, fmt.Errorf("%w: bad prefix %02X%02X", ErrBadPTZCmd, b[0], b[1])
	}
	sum := 0
	for i := 0; i < 7; i++ {
		sum += int(b[i])
	}
	if byte(sum) != b[7] {
		return ptz, fmt.Errorf("%w: got %02X want %02X", ErrPTZChecksum, b[7], byte(sum))
	}

	cmd := b[3]
	switch {
	case cmd >= 0x40:
		// 0x81~0x8x：预置位/巡航/扫描/灯光等扩展控制指令。
		return ptz, ErrUnsupported
	case cmd == 0:
		// 全停
	default:
		if cmd&0x01 != 0 {
			ptz.Pan = 1 // 右
		}
		if cmd&0x02 != 0 {
			ptz.Pan = -1 // 左
		}
		if cmd&0x04 != 0 {
			ptz.Tilt = -1 // 下
		}
		if cmd&0x08 != 0 {
			ptz.Tilt = 1 // 上
		}
		if cmd&0x10 != 0 {
			ptz.Zoom = 1 // 放大
		}
		if cmd&0x20 != 0 {
			ptz.Zoom = -1 // 缩小
		}
	}
	ptz.PanSpeed = int(b[4])
	ptz.TiltSpeed = int(b[5])
	ptz.ZoomSpeed = int(b[6] >> 4)
	return ptz, nil
}

// EncodePTZCmd 将 PTZ 动作编码为 A50F 指令串（主要用于测试与本地调试）。
func EncodePTZCmd(p PTZ) string {
	cmd := byte(0)
	if p.Pan > 0 {
		cmd |= 0x01
	} else if p.Pan < 0 {
		cmd |= 0x02
	}
	if p.Tilt > 0 {
		cmd |= 0x08
	} else if p.Tilt < 0 {
		cmd |= 0x04
	}
	if p.Zoom > 0 {
		cmd |= 0x10
	} else if p.Zoom < 0 {
		cmd |= 0x20
	}
	pan := clampByte(p.PanSpeed)
	tilt := clampByte(p.TiltSpeed)
	zoom := byte(p.ZoomSpeed&0x0F) << 4
	sum := byte(0xA5 + 0x0F + 0x01 + int(cmd) + int(pan) + int(tilt) + int(zoom))
	return fmt.Sprintf("A50F01%02X%02X%02X%02X%02X", cmd, pan, tilt, zoom, sum)
}

func clampByte(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}
