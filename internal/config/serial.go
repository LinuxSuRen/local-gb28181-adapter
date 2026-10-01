package config

import (
	"crypto/rand"
	"encoding/hex"
)

// randomSerial 生成 16 位十六进制设备序列号（首次启动时固化到配置文件）。
func randomSerial() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "ONVIFLOCAL000001"
	}
	return "OL-" + hex.EncodeToString(b)
}
