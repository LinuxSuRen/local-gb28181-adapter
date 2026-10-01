package ptzmock

import (
	"testing"
	"time"
)

func TestContinuousMoveIntegrates(t *testing.T) {
	n := NewNode()
	n.ContinuousMove(1, 0.5, 0)
	time.Sleep(300 * time.Millisecond)
	pan, tilt, zoom, moving := n.Status()
	if pan <= 10 {
		t.Fatalf("pan should have moved: %v", pan)
	}
	if tilt <= 0 {
		t.Fatalf("tilt should have moved: %v", tilt)
	}
	if zoom != 0 {
		t.Fatalf("zoom untouched: %v", zoom)
	}
	if !moving {
		t.Fatal("should be moving")
	}
}

func TestStopSemantics(t *testing.T) {
	n := NewNode()
	n.ContinuousMove(1, 1, 1)
	time.Sleep(100 * time.Millisecond)
	pan1, _, zoom1, _ := n.Status()
	n.Stop(false, true) // 只停 zoom
	time.Sleep(150 * time.Millisecond)
	pan2, _, zoom2, moving := n.Status()
	if pan2 <= pan1 {
		t.Fatalf("pan should keep moving after zoom-only stop: %v -> %v", pan1, pan2)
	}
	if zoom2-zoom1 > 1e-3 {
		t.Fatalf("zoom should be frozen: %v -> %v", zoom1, zoom2)
	}
	if !moving {
		t.Fatal("should still be moving (pan/tilt)")
	}
	n.Stop(true, true)
	_, _, _, moving = n.Status()
	if moving {
		t.Fatal("should be idle after full stop")
	}
}

func TestClampAtLimits(t *testing.T) {
	n := NewNode()
	n.AbsoluteMove(1000, -1000, 5)
	pan, tilt, zoom, _ := n.Status()
	if pan != PanMax || tilt != TiltMin || zoom != ZoomMax {
		t.Fatalf("clamp: pan=%v tilt=%v zoom=%v", pan, tilt, zoom)
	}
}

func TestRelativeMove(t *testing.T) {
	n := NewNode()
	n.AbsoluteMove(10, 5, 0.2)
	n.RelativeMove(5, -5, 0.1)
	pan, tilt, zoom, _ := n.Status()
	if pan != 15 || tilt != 0 || zoom-0.3 > 1e-9 {
		t.Fatalf("relative: %v %v %v", pan, tilt, zoom)
	}
}

func TestPresetsAndHome(t *testing.T) {
	n := NewNode()
	n.AbsoluteMove(20, 10, 0.4)
	n.SetHomePosition()
	tok := n.SetPreset("door")
	if tok == "" {
		t.Fatal("preset token empty")
	}
	// 同名覆盖。
	tok2 := n.SetPreset("door")
	if tok2 != tok {
		t.Fatalf("same-name preset should keep token: %q vs %q", tok2, tok)
	}
	n.AbsoluteMove(-30, 0, 0)
	if !n.GotoPreset(tok) {
		t.Fatal("goto preset failed")
	}
	pan, _, _, _ := n.Status()
	if pan != 20 {
		t.Fatalf("goto preset pan: %v", pan)
	}
	if !n.RemovePreset(tok) || n.GotoPreset(tok) {
		t.Fatal("preset should be removed")
	}
	n.GotoHomePosition()
	pan, tilt, zoom, _ := n.Status()
	if pan != 20 || tilt != 10 || zoom != 0.4 {
		t.Fatalf("home: %v %v %v", pan, tilt, zoom)
	}
}

func TestRegistryLifecycle(t *testing.T) {
	r := NewRegistry()
	a := r.Get("cam1")
	if r.Get("cam1") != a {
		t.Fatal("registry should reuse node")
	}
	r.Remove("cam1")
	if r.Get("cam1") == a {
		t.Fatal("registry should create new node after remove")
	}
}
