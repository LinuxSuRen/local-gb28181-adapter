package main

import (
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// freePort 借助 :0 拿一个空闲端口（关闭监听后立即返回，存在极小竞争窗口）。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen :0: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// freePortRange 找到 count 个连续空闲端口并保持占用（用于耗尽测试）。
func freePortRange(t *testing.T, count int) (int, []net.Listener) {
	t.Helper()
	for base := 20000; base < 40000; base++ {
		var listeners []net.Listener
		ok := true
		for i := 0; i < count; i++ {
			ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(base+i)))
			if err != nil {
				for _, l := range listeners {
					_ = l.Close()
				}
				ok = false
				break
			}
			listeners = append(listeners, ln)
		}
		if ok {
			return base, listeners
		}
	}
	t.Fatal("no free consecutive ports found")
	return 0, nil
}

func TestListenWithDriftFreePort(t *testing.T) {
	port := freePort(t)
	ln, got, drifted, err := listenWithDrift(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), maxPortDrift, quietLogger())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if got != port || drifted {
		t.Fatalf("want port %d without drift, got %d drifted=%v", port, got, drifted)
	}
}

func TestListenWithDriftBusyPort(t *testing.T) {
	// 占住 base，base+1 保持空闲。
	base, blockers := freePortRange(t, 1)
	defer func() {
		for _, l := range blockers {
			_ = l.Close()
		}
	}()
	ln, got, drifted, err := listenWithDrift(net.JoinHostPort("127.0.0.1", strconv.Itoa(base)), maxPortDrift, quietLogger())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if got != base+1 || !drifted {
		t.Fatalf("want drift to %d, got %d drifted=%v", base+1, got, drifted)
	}
}

func TestListenWithDriftExhausted(t *testing.T) {
	base, blockers := freePortRange(t, 2)
	defer func() {
		for _, l := range blockers {
			_ = l.Close()
		}
	}()
	_, _, _, err := listenWithDrift(net.JoinHostPort("127.0.0.1", strconv.Itoa(base)), 2, quietLogger())
	if err == nil {
		t.Fatal("want error when all candidates busy")
	}
}

func TestListenWithDriftInvalidAddr(t *testing.T) {
	if _, _, _, err := listenWithDrift("no-port-here", maxPortDrift, quietLogger()); err == nil {
		t.Fatal("want error for addr without port")
	}
	if _, _, _, err := listenWithDrift("127.0.0.1:notanumber", maxPortDrift, quietLogger()); err == nil {
		t.Fatal("want error for non-numeric port")
	}
}

func TestListenWithDriftWildcardHost(t *testing.T) {
	port := freePort(t)
	ln, got, drifted, err := listenWithDrift(":"+strconv.Itoa(port), maxPortDrift, quietLogger())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if got != port || drifted {
		t.Fatalf("wildcard host: got %d drifted=%v", got, drifted)
	}
}

func TestEnvHelpers(t *testing.T) {
	t.Setenv("GB_TEST_STR", "hello")
	if got := envOr("GB_TEST_STR", "def"); got != "hello" {
		t.Errorf("envOr = %q", got)
	}
	if got := envOr("GB_TEST_MISSING", "def"); got != "def" {
		t.Errorf("envOr default = %q", got)
	}

	t.Setenv("GB_TEST_INT", "42")
	if got := envInt("GB_TEST_INT", 7); got != 42 {
		t.Errorf("envInt = %d", got)
	}
	t.Setenv("GB_TEST_INT_BAD", "abc")
	if got := envInt("GB_TEST_INT_BAD", 7); got != 7 {
		t.Errorf("envInt bad = %d", got)
	}

	t.Setenv("GB_TEST_BOOL", "true")
	if !envBool("GB_TEST_BOOL", false) {
		t.Error("envBool true expected")
	}
	t.Setenv("GB_TEST_BOOL", "1")
	if !envBool("GB_TEST_BOOL", false) {
		t.Error("envBool 1 expected")
	}
	if envBool("GB_TEST_BOOL_MISSING", true) != true {
		t.Error("envBool default expected")
	}
}

func TestDetectAdvertiseIP(t *testing.T) {
	ip := detectAdvertiseIP(quietLogger())
	if ip == "" {
		t.Fatal("advertise ip empty")
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		t.Fatalf("invalid ip %q", ip)
	}
}
