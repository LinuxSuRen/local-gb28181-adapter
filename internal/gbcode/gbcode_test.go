package gbcode

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"34020000001320000001", true},
		{"34020000002000000001", true},
		{"3402000000132000000", false},  // 19 位
		{"3402000000132000000x", false}, // 非数字
		{"", false},
	}
	for _, c := range cases {
		if got := Valid(c.id); got != c.want {
			t.Errorf("Valid(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

func TestGenerate(t *testing.T) {
	if got := Generate("34020000", TypeIPC, 1); got != "34020000001320000001" {
		t.Errorf("Generate = %q", got)
	}
	// prefix 不足 8 位补零
	if got := Generate("3402", TypeCenter, 2); got != "34020000"+"00"+"200"+"0000002" {
		t.Errorf("Generate pad = %q", got)
	}
	// 序号溢出 7 位回绕
	if got := Generate("34020000", TypeIPC, 10000001); got != "34020000001320000001" {
		t.Errorf("Generate wrap = %q", got)
	}
}

func TestChannelID(t *testing.T) {
	got := ChannelID("34020000002000000001", 3)
	if got != "34020000001320000003" {
		t.Errorf("ChannelID = %q", got)
	}
	if got := ChannelID("", 1); got != "34020000001320000001" {
		t.Errorf("ChannelID default prefix = %q", got)
	}
}

func TestDomain(t *testing.T) {
	if got := Domain("34020000001320000003"); got != "3402000000" {
		t.Errorf("Domain = %q", got)
	}
}

func TestPTZCmdRoundTrip(t *testing.T) {
	cases := []PTZ{
		{Pan: 0, Tilt: 0, Zoom: 0, PanSpeed: 0, TiltSpeed: 0},
		{Pan: -1, PanSpeed: 0x50},
		{Pan: 1, PanSpeed: 0x50},
		{Tilt: 1, TiltSpeed: 0x50},
		{Tilt: -1, TiltSpeed: 0x50},
		{Zoom: 1, ZoomSpeed: 5},
		{Zoom: -1, ZoomSpeed: 5},
		{Pan: 1, Tilt: 1, PanSpeed: 10, TiltSpeed: 20, ZoomSpeed: 3},
	}
	for _, want := range cases {
		cmd := EncodePTZCmd(want)
		got, err := ParsePTZCmd(cmd)
		if err != nil {
			t.Fatalf("ParsePTZCmd(%s): %v", cmd, err)
		}
		if got != want {
			t.Errorf("round trip %s: got %+v want %+v", cmd, got, want)
		}
	}
}

func TestPTZCmdWVPCompatibility(t *testing.T) {
	// 与 WVP-GB28181-pro frontEndCmdString 生成的指令对齐：
	// 指令码位布局 bit0 右 / bit1 左 / bit2 下 / bit3 上 / bit4 放大 / bit5 缩小。
	// 校验码动态构造（校验码 = 前 7 字节之和 mod 256）。
	frame := func(b ...byte) string {
		s := 0
		for i := 0; i < 7; i++ {
			s += int(b[i])
		}
		b = append(b, byte(s))
		var sb strings.Builder
		for _, x := range b {
			const hexdigits = "0123456789ABCDEF"
			sb.WriteByte(hexdigits[x>>4])
			sb.WriteByte(hexdigits[x&0x0F])
		}
		return sb.String()
	}
	// 左：bit1
	if ptz, err := ParsePTZCmd(frame(0xA5, 0x0F, 0x01, 0x02, 0x05, 0x00, 0x00)); err != nil || ptz.Pan != -1 {
		t.Errorf("left: %+v err=%v", ptz, err)
	}
	// 右：bit0
	if ptz, err := ParsePTZCmd(frame(0xA5, 0x0F, 0x01, 0x01, 0x05, 0x00, 0x00)); err != nil || ptz.Pan != 1 {
		t.Errorf("right: %+v err=%v", ptz, err)
	}
	// 上：bit3
	if ptz, err := ParsePTZCmd(frame(0xA5, 0x0F, 0x01, 0x08, 0x00, 0x05, 0x00)); err != nil || ptz.Tilt != 1 {
		t.Errorf("up: %+v err=%v", ptz, err)
	}
	// 下：bit2
	if ptz, err := ParsePTZCmd(frame(0xA5, 0x0F, 0x01, 0x04, 0x00, 0x05, 0x00)); err != nil || ptz.Tilt != -1 {
		t.Errorf("down: %+v err=%v", ptz, err)
	}
	// 变倍放大：bit4；速度在组合码高 4 位
	if ptz, err := ParsePTZCmd(frame(0xA5, 0x0F, 0x01, 0x10, 0x00, 0x00, 0x40)); err != nil || ptz.Zoom != 1 || ptz.ZoomSpeed != 4 {
		t.Errorf("zoom in: %+v err=%v", ptz, err)
	}
	// 预置位等扩展指令：ErrUnsupported
	if _, err := ParsePTZCmd(frame(0xA5, 0x0F, 0x01, 0x81, 0x01, 0x00, 0x00)); err != ErrUnsupported {
		t.Errorf("preset: err=%v", err)
	}
}

func TestParsePTZCmdErrors(t *testing.T) {
	if _, err := ParsePTZCmd("A50F01"); err == nil {
		t.Error("short frame should fail")
	}
	if _, err := ParsePTZCmd("A50F0100000000FF"); err == nil { // 校验码错误
		t.Error("bad checksum should fail")
	}
	if _, err := ParsePTZCmd("000001020304050607"); err == nil {
		t.Error("bad prefix should fail")
	}
}
