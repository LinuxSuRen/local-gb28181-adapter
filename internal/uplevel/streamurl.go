package uplevel

import (
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
)

// rtspProbeTimeout 直通模式探测相机 RTSP 端口的超时。
const rtspProbeTimeout = 1200 * time.Millisecond

// ChannelSeq 从国标通道编码提取通道序号（末 7 位十进制，前导零忽略）。
// 34020000001320000001 → 1。
func ChannelSeq(channelID string) int {
	if len(channelID) < 7 {
		return 0
	}
	n, err := strconv.Atoi(channelID[len(channelID)-7:])
	if err != nil {
		return 0
	}
	return n
}

// CameraRTSPURL 按模板渲染相机的直通 RTSP 地址。
// 返回 false 表示模板未配置或模式非 direct（调用方应回退网关模式）。
func CameraRTSPURL(up config.Uplevel, deviceIP, channelID string) (string, bool) {
	if up.EffectiveStreamMode() != config.StreamModeDirect {
		return "", false
	}
	tpl := strings.TrimSpace(up.CameraRTSPTemplate)
	if tpl == "" {
		return "", false
	}
	r := strings.NewReplacer(
		"{ip}", deviceIP,
		"{n}", strconv.Itoa(ChannelSeq(channelID)),
	)
	return r.Replace(tpl), true
}

// ProbeTCPPort 探测 TCP 端口可达（直通模式可用性判断）。
func ProbeTCPPort(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), rtspProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ResolveStreamURL 综合判定对外取流地址（模式决策核心）：
//  1. direct 模式且模板已配置且相机 RTSP 端口可达 → 直通地址；
//  2. 否则回退网关模式（适配器内嵌 RTSP，由调用方拼接并自动点播）。
func ResolveStreamURL(up config.Uplevel, deviceIP, channelID, gatewayURL string) (url string, direct bool) {
	if cameraURL, ok := CameraRTSPURL(up, deviceIP, channelID); ok && ProbeTCPPort(deviceIP, 554) {
		return cameraURL, true
	}
	return gatewayURL, false
}
