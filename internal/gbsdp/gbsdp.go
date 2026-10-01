// Package gbsdp 实现 GB/T 28181 点播 SDP（INVITE 请求与 200 应答）的解析与构造。
// 支持 UDP 与 TCP 被动（平台监听、设备主动连接）两种收流模式。
package gbsdp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MediaTransport 媒体传输模式。
type MediaTransport string

// 支持的传输模式。
const (
	TransportUDP         MediaTransport = "UDP"
	TransportTCPPassive  MediaTransport = "TCP-PASSIVE" // 平台监听，设备主动连接
	TransportTCPActive   MediaTransport = "TCP-ACTIVE"  // 平台主动连接设备（设备监听）
)

// Offer 平台 INVITE 携带的 SDP 解析结果。
type Offer struct {
	ChannelID  string // o= 行用户名（被点播通道）
	Session    string // s= 行：Play 实时点播 / Playback 回放 / Download 下载
	IP         string // c= 行媒体地址（平台收流地址）
	Port       int    // m= 行端口
	Transport  MediaTransport
	SSRC       uint32 // y= 行（十进制字符串）
	SSRCString string
}

// ErrInvalidSDP SDP 无法解析。
var ErrInvalidSDP = errors.New("invalid gb28181 sdp")

// ErrUnsupportedTransport 不支持的传输模式（如 TCP-ACTIVE）。
var ErrUnsupportedTransport = errors.New("unsupported transport mode")

// ParseOffer 解析平台 INVITE 的 SDP。
func ParseOffer(body string) (Offer, error) {
	var o Offer
	var proto string
	setup := ""
	for _, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "o="):
			// o=<username> <sess-id> <version> IN IP4 <addr>
			fields := strings.Fields(strings.TrimPrefix(line, "o="))
			if len(fields) >= 6 {
				o.ChannelID = fields[0]
			} else if len(fields) > 0 {
				o.ChannelID = fields[0]
			}
		case strings.HasPrefix(line, "s="):
			o.Session = strings.TrimSpace(strings.TrimPrefix(line, "s="))
		case strings.HasPrefix(line, "c=IN IP4 "):
			o.IP = strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4 "))
		case strings.HasPrefix(line, "m=video"):
			// m=video <port> <proto> <fmt...>
			fields := strings.Fields(strings.TrimPrefix(line, "m=video"))
			if len(fields) < 2 {
				return o, fmt.Errorf("%w: bad m line %q", ErrInvalidSDP, line)
			}
			port, err := strconv.Atoi(fields[0])
			if err != nil {
				return o, fmt.Errorf("%w: bad port %q", ErrInvalidSDP, fields[0])
			}
			o.Port = port
			proto = fields[1]
		case strings.HasPrefix(line, "a=setup:"):
			setup = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "a=setup:")))
		case strings.HasPrefix(line, "y="):
			o.SSRCString = strings.TrimSpace(strings.TrimPrefix(line, "y="))
		}
	}
	if o.IP == "" {
		return o, fmt.Errorf("%w: no c= line", ErrInvalidSDP)
	}
	if o.Port <= 0 {
		return o, fmt.Errorf("%w: no m=video line", ErrInvalidSDP)
	}
	if o.SSRCString != "" {
		v, err := strconv.ParseUint(o.SSRCString, 10, 32)
		if err != nil {
			return o, fmt.Errorf("%w: bad y= ssrc %q", ErrInvalidSDP, o.SSRCString)
		}
		o.SSRC = uint32(v)
	}

	switch {
	case strings.HasPrefix(proto, "TCP"):
		// setup 语义：offer 的 a=setup 描述平台角色：
		//   passive = 平台监听，设备连接（本适配器支持）；
		//   active  = 平台主动连接设备（需要设备监听，首版不支持）。
		if setup == "active" {
			return o, fmt.Errorf("%w: tcp active (platform connects)", ErrUnsupportedTransport)
		}
		o.Transport = TransportTCPPassive
	default:
		o.Transport = TransportUDP
	}
	return o, nil
}

// BuildAnswer 构造 200 OK 应答 SDP。
// deviceIP 为本机对外 IP，localPort 为本端媒体端口（不接收时仅作展示），
// transport/SSRC 取自解析出的 offer。
func BuildAnswer(channelID, deviceIP string, localPort int, transport MediaTransport, ssrc uint32) string {
	proto := "RTP/AVP"
	setup := ""
	if transport == TransportTCPPassive {
		proto = "TCP/RTP/AVP"
		// 平台 passive，设备应答 active。
		setup = "a=setup:active\r\n"
	}
	ssrcStr := strconv.FormatUint(uint64(ssrc), 10)
	var sb strings.Builder
	sb.WriteString("v=0\r\n")
	fmt.Fprintf(&sb, "o=%s 0 0 IN IP4 %s\r\n", channelID, deviceIP)
	sb.WriteString("s=Play\r\n")
	fmt.Fprintf(&sb, "c=IN IP4 %s\r\n", deviceIP)
	sb.WriteString("t=0 0\r\n")
	fmt.Fprintf(&sb, "m=video %d %s 96\r\n", localPort, proto)
	sb.WriteString("a=sendonly\r\n")
	sb.WriteString(setup)
	fmt.Fprintf(&sb, "y=%s\r\n", ssrcStr)
	return sb.String()
}
