package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxsuren/local-gb28181-adapter/internal/gbcode"
)

func TestLoadStoreDefaults(t *testing.T) {
	dir := t.TempDir()
	store, err := LoadStore(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	root := store.Root()
	if !gbcode.Valid(root.Platform.ServerID) {
		t.Errorf("default server id invalid: %q", root.Platform.ServerID)
	}
	if root.Platform.DeviceID == "" || !gbcode.Valid(root.Platform.DeviceID) {
		t.Errorf("default device id invalid: %q", root.Platform.DeviceID)
	}
	if root.Platform.Domain() != root.Platform.DeviceID[:10] {
		t.Errorf("domain = %q", root.Platform.Domain())
	}
	if root.Server.RTSPEmbedded != true || root.Server.HTTPAddr != ":8080" {
		t.Errorf("server defaults: %+v", root.Server)
	}
	// 文件已落盘
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Errorf("config not persisted: %v", err)
	}
}

func TestCameraChannelIDDerivation(t *testing.T) {
	root := Root{
		Platform: Platform{DeviceID: "34020000002000000002"},
		Cameras: []Camera{
			{ID: "1", Name: "cam1"},
			{ID: "2", Name: "cam2", ChannelID: "34020000001321000099"},
		},
	}
	if got := root.Cameras[0].GBChannelID(root.Platform.DeviceID); got != "34020000001320000001" {
		t.Errorf("derived channel id = %q", got)
	}
	if got := root.Cameras[1].GBChannelID(root.Platform.DeviceID); got != "34020000001321000099" {
		t.Errorf("explicit channel id = %q", got)
	}
	// 非数字 ID 且未手填时 FNV 兜底派生：稳定且合法
	idA := (Camera{ID: "abc"}).GBChannelID(root.Platform.DeviceID)
	idB := (Camera{ID: "abc"}).GBChannelID(root.Platform.DeviceID)
	if idA == "" || idA != idB || !gbcode.Valid(idA) {
		t.Errorf("non-numeric id channel should derive stably, got %q vs %q", idA, idB)
	}
	if idA == root.Cameras[0].GBChannelID(root.Platform.DeviceID) {
		t.Errorf("non-numeric id channel collides with cam1: %q", idA)
	}
	// 按通道编码反查
	if cam, ok := root.FindCameraByChannel("34020000001320000001", root.Platform.DeviceID); !ok || cam.ID != "1" {
		t.Errorf("find by channel: %+v ok=%v", cam, ok)
	}
	if _, ok := root.FindCameraByChannel("34020000001329999999", root.Platform.DeviceID); ok {
		t.Error("unknown channel should not match")
	}
}

func TestNormalizeResetsInvalidChannelID(t *testing.T) {
	r := Default()
	r.Cameras = []Camera{{ID: "1", ChannelID: "bad"}}
	r.normalize()
	if r.Cameras[0].ChannelID != "" {
		t.Errorf("invalid channel id should reset, got %q", r.Cameras[0].ChannelID)
	}
}

func TestUpdateTransactionRollback(t *testing.T) {
	store, err := LoadStore(t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	err = store.Update(func(r *Root) error {
		r.Platform.Password = "x"
		return os.ErrPermission
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if store.Root().Platform.Password != "" {
		t.Error("rollback failed")
	}
}
