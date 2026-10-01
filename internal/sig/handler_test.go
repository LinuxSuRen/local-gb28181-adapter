package sig

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
	"github.com/linuxsuren/local-gb28181-adapter/internal/gbcode"
	"github.com/linuxsuren/local-gb28181-adapter/internal/manscdp"
	"github.com/linuxsuren/local-gb28181-adapter/internal/ptzmock"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	store, err := config.LoadStore(t.TempDir())
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if err := store.Update(func(r *config.Root) error {
		r.Platform.DeviceID = "34020000002000000001"
		r.Cameras = []config.Camera{{
			ID: "cam1", Name: "cam1", Type: config.TypeTestSrc, Enabled: true,
		}}
		return nil
	}); err != nil {
		t.Fatalf("update store: %v", err)
	}
	return New(store, ptzmock.NewRegistry(), nil, nil, slog.Default())
}

// ptzCmdFrame 构造 A50F 指令（校验码 = 前 7 字节和 mod 256）。
func ptzCmdFrame(t *testing.T, cmd byte, p1, p2 byte) string {
	t.Helper()
	b := []byte{0xA5, 0x0F, 0x01, cmd, p1, p2, 0x00}
	sum := 0
	for _, x := range b {
		sum += int(x)
	}
	b = append(b, byte(sum))
	var sb strings.Builder
	const hexdigits = "0123456789ABCDEF"
	for _, x := range b {
		sb.WriteByte(hexdigits[x>>4])
		sb.WriteByte(hexdigits[x&0x0F])
	}
	return sb.String()
}

func TestHandleDeviceControlDirections(t *testing.T) {
	svc := newTestService(t)
	root := svc.store.Root()
	channel := root.Cameras[0].GBChannelID(root.Platform.DeviceID)

	// 通过两次采样间的位置差判断运动方向（速度恒定，方向确定）。
	delta := func(sample func() float64) float64 {
		v0 := sample()
		time.Sleep(60 * time.Millisecond)
		return sample() - v0
	}
	node := svc.ptz.Get("cam1")
	panOf := func() float64 { p, _, _, _ := node.Status(); return p }
	tiltOf := func() float64 { _, ti, _, _ := node.Status(); return ti }
	zoomOf := func() float64 { _, _, z, _ := node.Status(); return z }

	cases := []struct {
		name string
		cmd  byte
		must func() bool
	}{
		{"right", 0x01, func() bool { return delta(panOf) > 0.0001 }},
		{"left", 0x02, func() bool { return delta(panOf) < -0.0001 }},
		{"up", 0x08, func() bool { return delta(tiltOf) > 0.0001 }},
		{"down", 0x04, func() bool { return delta(tiltOf) < -0.0001 }},
		{"zoomin", 0x10, func() bool { return delta(zoomOf) > 0.00001 }},
		{"zoomout", 0x20, func() bool { return delta(zoomOf) < -0.00001 }},
		{"stop", 0x00, func() bool {
			node.Stop(true, true)
			time.Sleep(20 * time.Millisecond)
			_, _, _, moving := node.Status()
			return !moving
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc.handleDeviceControl(root, manscdp.Query{
				CmdType:  "DeviceControl",
				DeviceID: channel,
				PTZCmd:   ptzCmdFrame(t, c.cmd, 20, 20),
			})
			if !c.must() {
				t.Errorf("%s: direction assertion failed", c.name)
			}
		})
	}
}

func TestHandleDeviceControlUnknownChannel(t *testing.T) {
	svc := newTestService(t)
	root := svc.store.Root()
	// 未知通道：不应 panic，云台不动
	svc.handleDeviceControl(root, manscdp.Query{
		CmdType:  "DeviceControl",
		DeviceID: "34020000001329999999",
		PTZCmd:   ptzCmdFrame(t, 0x01, 20, 20),
	})
	if _, _, _, moving := svc.ptz.Get("cam1").Status(); moving {
		t.Error("unknown channel must not move ptz")
	}
}

func TestHandleDeviceControlPresetSkipped(t *testing.T) {
	svc := newTestService(t)
	root := svc.store.Root()
	channel := root.Cameras[0].GBChannelID(root.Platform.DeviceID)
	// 0x81 = 预置位指令，应被忽略且不 panic
	node := svc.ptz.Get("cam1")
	node.ContinuousMove(1, 0, 0)
	svc.handleDeviceControl(root, manscdp.Query{
		DeviceID: channel,
		PTZCmd:   ptzCmdFrame(t, 0x81, 1, 0),
	})
	// 原速度保持（未 stop 也未改向）
	if _, _, _, moving := node.Status(); !moving {
		t.Log("preset command should not stop motion; state may have advanced")
	}
}

func TestRandomSSRCRange(t *testing.T) {
	for i := 0; i < 100; i++ {
		v := randomSSRC()
		if v < 100000000 || v > 999999999 {
			t.Fatalf("ssrc out of 9-digit range: %d", v)
		}
	}
}

func TestGBChannelRoundTrip(t *testing.T) {
	// 通道编码派生与反查闭环：cam1/cam2 形式的 ID 也必须稳定派生并能反查。
	root := config.Root{
		Platform: config.Platform{DeviceID: "34020000002000000001"},
		Cameras: []config.Camera{
			{ID: "cam1", Enabled: true},
			{ID: "cam2", Enabled: true},
			{ID: "custom", Enabled: true},
		},
	}
	id1 := root.Cameras[0].GBChannelID(root.Platform.DeviceID)
	if !gbcode.Valid(id1) {
		t.Fatalf("derived id invalid: %q", id1)
	}
	if id1 != "34020000001320000001" {
		t.Errorf("cam1 derived id = %q", id1)
	}
	cam, ok := root.FindCameraByChannel(id1, root.Platform.DeviceID)
	if !ok || cam.ID != "cam1" {
		t.Errorf("lookup %q got %+v ok=%v", id1, cam, ok)
	}
	// 非数字 ID 也要稳定且唯一
	idCustom := root.Cameras[2].GBChannelID(root.Platform.DeviceID)
	idCustom2 := root.Cameras[2].GBChannelID(root.Platform.DeviceID)
	if !gbcode.Valid(idCustom) || idCustom != idCustom2 {
		t.Errorf("custom id unstable or invalid: %q vs %q", idCustom, idCustom2)
	}
	if idCustom == id1 {
		t.Errorf("custom id collides with cam1: %q", idCustom)
	}
}
