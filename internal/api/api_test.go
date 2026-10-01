package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
	"github.com/linuxsuren/local-gb28181-adapter/internal/ptzmock"
	"github.com/linuxsuren/local-gb28181-adapter/internal/sig"
	"github.com/linuxsuren/local-gb28181-adapter/internal/snapshot"
	"github.com/linuxsuren/local-gb28181-adapter/internal/stream"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store, err := config.LoadStore(t.TempDir())
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	gb := sig.New(store, ptzmock.NewRegistry(), nil, nil, slog.Default())
	srv := NewServer(store, ptzmock.NewRegistry(),
		stream.NewManager("rtsp://127.0.0.1:8554", "ffmpeg", slog.Default()),
		snapshot.New("ffmpeg", slog.Default()), gb, "test", slog.Default())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, payload
}

func doJSON(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, payload
}

func dataOf(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	d, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("payload without data object: %+v", payload)
	}
	return d
}

func TestSystemEndpoint(t *testing.T) {
	_, ts := newTestServer(t)
	status, payload := getJSON(t, ts.URL+"/api/system")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	data := dataOf(t, payload)
	if data["version"] != "test" {
		t.Errorf("version = %v", data["version"])
	}
	if data["device_id"] == "" {
		t.Error("device_id should be present")
	}
	reg, ok := data["registration"].(map[string]any)
	if !ok {
		t.Fatalf("registration missing: %+v", data)
	}
	if reg["state"] != "idle" {
		t.Errorf("initial state = %v", reg["state"])
	}
}

func TestPlatformGetPut(t *testing.T) {
	srv, ts := newTestServer(t)
	status, payload := getJSON(t, ts.URL+"/api/platform")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	data := dataOf(t, payload)
	if data["server_addr"] != "" {
		t.Errorf("default server addr should be empty, got %v", data["server_addr"])
	}

	// 保存平台配置
	status, payload = doJSON(t, "PUT", ts.URL+"/api/platform", `{
		"enabled": true,
		"server_id": "34020000002000000001",
		"server_addr": "192.168.1.10:5060",
		"device_id": "34020000002000000002",
		"password": "secret",
		"register_expires": 3600,
		"keepalive_interval": 60
	}`)
	if status != http.StatusOK {
		t.Fatalf("put status = %d payload=%+v", status, payload)
	}
	data = dataOf(t, payload)
	if data["server_addr"] != "192.168.1.10:5060" {
		t.Errorf("server_addr = %v", data["server_addr"])
	}
	if data["domain"] != "3402000000" {
		t.Errorf("domain = %v", data["domain"])
	}
	if v, _ := data["password_set"].(bool); !v {
		t.Error("password_set should be true")
	}
	// OnPlatformChange 回调触发
	if srv.OnPlatformChange == nil {
		root := srv.store.Root()
		if !root.Platform.Enabled {
			t.Error("platform should be enabled in store")
		}
	}
}

func TestPlatformValidation(t *testing.T) {
	_, ts := newTestServer(t)
	cases := []struct {
		name string
		body string
	}{
		{"bad server id", `{"server_id": "123"}`},
		{"bad device id", `{"device_id": "abc"}`},
		{"bad addr", `{"server_addr": "no-port"}`},
		{"bad expires", `{"register_expires": 1}`},
		{"bad keepalive", `{"keepalive_interval": 1}`},
		{"enable without addr", `{"enabled": true}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, payload := doJSON(t, "PUT", ts.URL+"/api/platform", c.body)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d payload = %+v", status, payload)
			}
		})
	}
}

func TestCameraChannelIDFlow(t *testing.T) {
	_, ts := newTestServer(t)
	// 创建摄像头
	status, payload := doJSON(t, "POST", ts.URL+"/api/cameras",
		`{"name":"测试","type":"testsrc","source":"","enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("create status = %d payload=%+v", status, payload)
	}
	data := dataOf(t, payload)
	if data["id"] == "" {
		t.Fatal("camera id missing")
	}
	st, ok := data["status"].(map[string]any)
	if !ok || st["channel_id"] == "" || len(st["channel_id"].(string)) != 20 {
		t.Fatalf("channel_id missing or invalid: %+v", data)
	}
	channel := st["channel_id"].(string)

	// 会话列表初始为空数组
	status, payload = getJSON(t, ts.URL+"/api/sessions")
	if status != http.StatusOK {
		t.Fatalf("sessions status = %d", status)
	}
	arr, ok := payload["data"].([]any)
	if !ok || len(arr) != 0 {
		t.Errorf("sessions should be empty array: %+v", payload)
	}

	// 更新手填通道编码
	id := data["id"].(string)
	status, _ = doJSON(t, "PUT", ts.URL+"/api/cameras/"+id,
		`{"name":"测试","type":"testsrc","source":"","channel_id":"34020000001321000099"}`)
	if status != http.StatusOK {
		t.Fatalf("update status = %d", status)
	}
	_, payload = getJSON(t, ts.URL+"/api/cameras")
	list, _ := payload["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("camera list len = %d", len(list))
	}
	cam := list[0].(map[string]any)
	if cam["channel_id"] != "34020000001321000099" {
		t.Errorf("channel_id = %v", cam["channel_id"])
	}

	// 非法通道编码被拒
	status, _ = doJSON(t, "PUT", ts.URL+"/api/cameras/"+id,
		`{"name":"测试","type":"testsrc","source":"","channel_id":"abc"}`)
	if status != http.StatusBadRequest {
		t.Errorf("invalid channel id should be 400, got %d", status)
	}
	_ = channel
}

func TestHealthz(t *testing.T) {
	_, ts := newTestServer(t)
	status, payload := getJSON(t, ts.URL+"/healthz")
	if status != http.StatusOK || payload["data"].(map[string]any)["status"] != "ok" {
		t.Errorf("healthz: %d %+v", status, payload)
	}
}
