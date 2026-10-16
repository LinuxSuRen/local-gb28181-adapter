package onvif

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strings"
	"syscall"
	"time"

	golangIPv4 "golang.org/x/net/ipv4"
)

// discoveryAddr WS-Discovery 组播地址。
const discoveryAddr = "239.255.255.250:3702"

var messageIDRe = regexp.MustCompile(`<[^>]*:MessageID[^>]*>([^<]+)<`)

// StartDiscovery 启动 WS-Discovery 应答循环（阻塞）：
// 以 SO_REUSEADDR 绑定 3702（与本机其它发现服务共存），全网卡加组，
// 对 Probe 回 ProbeMatches；端口绑定失败时降级为周期性组播 Hello。
func (s *Server) StartDiscovery(stop <-chan struct{}) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stop
		cancel()
	}()

	conn, err := listenReusable()
	if err != nil {
		s.logger.Warn("ws-discovery port 3702 busy, fallback to periodic Hello", "err", err.Error())
		s.runHelloFallback(ctx)
		return nil
	}
	defer func() { _ = conn.Close() }()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	gaddr := mustResolve(discoveryAddr)
	pconn := golangIPv4.NewPacketConn(conn)
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagMulticast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if err := pconn.JoinGroup(&ifc, gaddr); err == nil {
			s.logger.Debug("ws-discovery joined group", "interface", ifc.Name)
		}
	}
	s.logger.Info("ws-discovery responder started", "multicast", discoveryAddr)

	buf := make([]byte, 8192)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if ctx.Err() != nil {
					return nil
				}
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			s.logger.Warn("ws-discovery read failed", "err", err.Error())
			continue
		}
		msg := string(buf[:n])
		// 只响应 Probe（忽略 Hello/Bye/Resolve 等其它发现消息）。
		if !strings.Contains(msg, ":Probe") && !strings.Contains(msg, "Probe>") {
			continue
		}
		resp := s.probeMatch(extractMessageID(msg))
		if _, err := conn.WriteToUDP([]byte(resp), from); err != nil {
			s.logger.Warn("ws-discovery reply failed", "err", err.Error())
		} else {
			s.logger.Debug("ws-discovery probematch sent", "to", from.String())
		}
	}
}

// listenReusable 以 SO_REUSEADDR 绑定组播端口。
func listenReusable() (*net.UDPConn, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var sockErr error
			err := c.Control(func(fd uintptr) {
				sockErr = setReuseAddr(fd)
			})
			if err != nil {
				return err
			}
			return sockErr
		},
	}
	pc, err := lc.ListenPacket(context.Background(), "udp4", discoveryAddr)
	if err != nil {
		return nil, err
	}
	udp, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("unexpected connection type %T", pc)
	}
	return udp, nil
}

// runHelloFallback 端口被占用时周期性组播 Hello（每 30 秒），
// 客户端仍可通过 Hello 感知本服务（详见姊妹项目 local-onvif-adapter 同款方案）。
func (s *Server) runHelloFallback(ctx context.Context) {
	conn, err := net.DialUDP("udp4", nil, mustResolve(discoveryAddr))
	if err != nil {
		s.logger.Error("discovery hello fallback failed", "err", err.Error())
		return
	}
	defer func() { _ = conn.Close() }()
	s.logger.Info("ws-discovery hello fallback started", "interval", "30s")
	var seq int64
	for {
		if _, err := conn.Write([]byte(s.hello(seq))); err != nil {
			s.logger.Warn("discovery hello send failed", "err", err.Error())
		}
		seq++
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// probeMatch 构造 ProbeMatches 应答（全部国标通道入口）。
func (s *Server) probeMatch(relatesTo string) string {
	var xaddrs, scopes []string
	scopes = append(scopes,
		"onvif://www.onvif.org/type/NetworkVideoTransmitter",
		"onvif://www.onvif.org/type/video_encoder",
		"onvif://www.onvif.org/type/ptz",
		"onvif://www.onvif.org/Profile/Streaming",
		"onvif://www.onvif.org/name/local-gb28181-adapter")
	for _, dev := range s.up.Registry.Snapshot() {
		for _, ch := range dev.Channels {
			xaddrs = append(xaddrs, s.serviceURL(ch.DeviceID))
		}
	}
	if len(xaddrs) == 0 {
		// 目录未就绪时也给出服务根入口，避免客户端空手而归。
		xaddrs = append(xaddrs, s.baseURL()+"/onvif/device_list")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope"` +
		` xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing"` +
		` xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"` +
		` xmlns:dn="http://www.onvif.org/ver10/network/wsdl">` +
		`<SOAP-ENV:Header>` +
		`<wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</wsa:Action>` +
		`<wsa:RelatesTo>` + xmlEscape(relatesTo) + `</wsa:RelatesTo>` +
		`<wsa:To>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</wsa:To>` +
		`<wsa:MessageID>urn:uuid:` + newUUID() + `</wsa:MessageID>` +
		fmt.Sprintf(`<d:AppSequence InstanceId="%d" MessageNumber="%d"/>`, 1, time.Now().UnixNano()%1e9) +
		`</SOAP-ENV:Header>` +
		`<SOAP-ENV:Body><d:ProbeMatches><d:ProbeMatch>` +
		`<wsa:EndpointReference><wsa:Address>urn:uuid:local-gb28181-adapter</wsa:Address></wsa:EndpointReference>` +
		`<d:Types>dn:NetworkVideoTransmitter</d:Types>` +
		`<d:Scopes>` + xmlEscape(strings.Join(scopes, " ")) + `</d:Scopes>` +
		`<d:XAddrs>` + xmlEscape(strings.Join(xaddrs, " ")) + `</d:XAddrs>` +
		`<d:MetadataVersion>1</d:MetadataVersion>` +
		`</d:ProbeMatch></d:ProbeMatches></SOAP-ENV:Body></SOAP-ENV:Envelope>`
}

// hello 周期性组播 Hello（fallback 用）。
func (s *Server) hello(seq int64) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope"` +
		` xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing"` +
		` xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"` +
		` xmlns:dn="http://www.onvif.org/ver10/network/wsdl">` +
		`<SOAP-ENV:Header>` +
		`<wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Hello</wsa:Action>` +
		`<wsa:MessageID>urn:uuid:` + newUUID() + `</wsa:MessageID>` +
		`<wsa:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</wsa:To>` +
		fmt.Sprintf(`<d:AppSequence InstanceId="%d" MessageNumber="%d"/>`, 1, seq) +
		`</SOAP-ENV:Header>` +
		`<SOAP-ENV:Body><d:Hello>` +
		`<wsa:EndpointReference><wsa:Address>urn:uuid:local-gb28181-adapter</wsa:Address></wsa:EndpointReference>` +
		`<d:Types>dn:NetworkVideoTransmitter</d:Types>` +
		`<d:Scopes>onvif://www.onvif.org/name/local-gb28181-adapter</d:Scopes>` +
		`<d:XAddrs>` + xmlEscape(s.baseURL()+"/onvif/device_list") + `</d:XAddrs>` +
		`<d:MetadataVersion>1</d:MetadataVersion>` +
		`</d:Hello></SOAP-ENV:Body></SOAP-ENV:Envelope>`
}

func extractMessageID(msg string) string {
	if m := messageIDRe.FindStringSubmatch(msg); len(m) > 1 {
		return m[1]
	}
	return "urn:unknown"
}

func mustResolve(addr string) *net.UDPAddr {
	a, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}
	}
	return a
}

// newUUID 生成随机 UUID v4 文本。
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixMilli())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}
