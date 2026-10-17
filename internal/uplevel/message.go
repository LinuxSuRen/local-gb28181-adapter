package uplevel

import (
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/linuxsuren/local-gb28181-adapter/internal/gbcode"
	"github.com/linuxsuren/local-gb28181-adapter/internal/manscdp"
)

// queryTimeout 平台查询等待设备应答的超时。
const queryTimeout = 6 * time.Second

// decodeBody 处理国产设备常见的 GB2312/GBK/GB18030 编码声明：
// Go XML 解码器无内置 charset 支持，检测到 GB 系编码声明时统一转码为 UTF-8，
// 并同步改写声明，避免解码器继续按 GB 编码报错。
func decodeBody(body []byte) []byte {
	head := body
	if len(head) > 256 {
		head = head[:256]
	}
	upper := strings.ToUpper(string(head))
	declared := ""
	for _, enc := range []string{"GB18030", "GB2312", "GBK"} {
		if strings.Contains(upper, enc) {
			declared = enc
			break
		}
	}
	if declared == "" {
		return body
	}
	out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(body)
	if err != nil {
		return body
	}
	replacement := "encoding=\"UTF-8\""
	idx := strings.Index(strings.ToUpper(string(out)), "ENCODING=")
	if idx < 0 {
		return out
	}
	end := strings.IndexByte(string(out[idx:]), '?')
	if end < 0 {
		return out
	}
	return []byte(string(out[:idx]) + replacement + string(out[idx+end:]))
}

// onMessage 处理下级设备发来的 MESSAGE：心跳上报与查询应答（目录/设备信息）。
func (s *Service) onMessage(req *sip.Request, tx sip.ServerTransaction) {
	body := decodeBody(req.Body())
	// 判定根元素：Notify（心跳）/ Response（查询应答）/ 其他忽略。
	var head struct {
		XMLName  xml.Name
		CmdType  string `xml:"CmdType"`
		SN       int    `xml:"SN"`
		DeviceID string `xml:"DeviceID"`
	}
	if err := xml.Unmarshal(body, &head); err != nil {
		s.logger.Warn("uplevel bad message body", "err", err.Error())
		_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Message", nil))
		return
	}
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))

	// 设备身份优先取 body DeviceID，兜底 From 头。
	deviceID := head.DeviceID
	if deviceID == "" && req.From() != nil {
		deviceID = req.From().Address.User
	}

	switch head.XMLName.Local {
	case "Notify":
		if head.CmdType != "Keepalive" {
			s.logger.Debug("uplevel notify ignored", "cmd", head.CmdType)
			return
		}
		ip, port := sourceAddr(req)
		s.Registry.Keepalive(deviceID, ip, port)
		s.logger.Debug("uplevel keepalive", "device", deviceID)
	case "Response":
		key := fmt.Sprintf("%s:%d", deviceID, head.SN)
		s.mu.Lock()
		ch, ok := s.pending[key]
		if ok {
			delete(s.pending, key)
		}
		s.mu.Unlock()
		switch head.CmdType {
		case "Catalog":
			resp, err := manscdp.ParseCatalogResponse(body)
			if err != nil {
				s.logger.Warn("uplevel catalog response parse failed", "err", err.Error())
				return
			}
			chs := make([]ChannelInfo, 0, len(resp.DeviceList.Items))
			for _, it := range resp.DeviceList.Items {
				chs = append(chs, ChannelInfo{
					DeviceID:     it.DeviceID,
					Name:         it.Name,
					Status:       it.Status,
					Manufacturer: it.Manufacturer,
					Model:        it.Model,
				})
			}
			s.Registry.UpdateChannels(deviceID, chs)
			s.logger.Info("uplevel catalog received",
				"device", deviceID, "sum", resp.SumNum, "channels", len(chs))
			if ok {
				ch <- resp
			}
		case "DeviceInfo":
			resp, err := manscdp.ParseDeviceInfoResponse(body)
			if err != nil {
				s.logger.Warn("uplevel deviceinfo response parse failed", "err", err.Error())
				return
			}
			s.Registry.UpdateInfo(deviceID, &resp)
			s.logger.Info("uplevel deviceinfo received",
				"device", deviceID, "model", resp.Model, "firmware", resp.Firmware)
			if ok {
				ch <- resp
			}
		default:
			s.logger.Debug("uplevel response ignored", "cmd", head.CmdType)
		}
	default:
		s.logger.Debug("uplevel message ignored", "root", head.XMLName.Local, "cmd", head.CmdType)
	}
}

// sourceAddr 取请求源地址。
func sourceAddr(req *sip.Request) (string, int) {
	if src := req.Source(); src != "" {
		if h, p, err := net.SplitHostPort(src); err == nil {
			if v, err := strconv.Atoi(p); err == nil {
				return h, v
			}
		}
	}
	return "127.0.0.1", 5060
}

// sendMessage 发送 MESSAGE 到设备并等待最终应答。
func (s *Service) sendMessage(ctx context.Context, deviceID, body string) error {
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	if client == nil {
		return fmt.Errorf("uplevel not running")
	}
	dev, ok := s.Registry.Find(deviceID)
	if !ok {
		return fmt.Errorf("device %s not registered", deviceID)
	}
	up := s.store.Root().Uplevel
	recipient := sip.Uri{Scheme: "sip", User: deviceID, Host: dev.RemoteIP, Port: dev.RemotePort}
	req := sip.NewRequest(sip.MESSAGE, recipient)
	req.SetTransport("UDP")
	req.AppendHeader(sip.NewHeader("From", fmt.Sprintf("<sip:%s@%s>", up.ServerID, up.Realm())))
	req.AppendHeader(sip.NewHeader("To", fmt.Sprintf("<sip:%s@%s>", deviceID, up.Realm())))
	req.SetBody([]byte(body))
	req.AppendHeader(sip.NewHeader("Content-Type", "Application/MANSRTSP"))

	tx, err := client.TransactionRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	defer tx.Terminate()
	for {
		select {
		case res := <-tx.Responses():
			if res.IsProvisional() {
				continue
			}
			if res.StatusCode/100 != 2 {
				return fmt.Errorf("message rejected: %d %s", res.StatusCode, res.Reason)
			}
			return nil
		case <-tx.Done():
			return fmt.Errorf("transaction done: %w", tx.Err())
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// sendQuery 下发查询并登记等待者，返回等待 channel。
func (s *Service) sendQuery(ctx context.Context, deviceID, cmdType string) (chan any, error) {
	sn := s.nextSN()
	body := manscdp.BuildQuery(cmdType, sn, deviceID)
	key := fmt.Sprintf("%s:%d", deviceID, sn)
	ch := make(chan any, 1)
	s.mu.Lock()
	s.pending[key] = ch
	s.mu.Unlock()
	if err := s.sendMessage(ctx, deviceID, body); err != nil {
		s.mu.Lock()
		delete(s.pending, key)
		s.mu.Unlock()
		return nil, err
	}
	return ch, nil
}

// QueryCatalog 查询设备目录并等待应答。
func (s *Service) QueryCatalog(ctx context.Context, deviceID string) ([]ChannelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	ch, err := s.sendQuery(ctx, deviceID, "Catalog")
	if err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		cat, ok := resp.(manscdp.CatalogResponse)
		if !ok {
			return nil, fmt.Errorf("unexpected catalog response type")
		}
		out := make([]ChannelInfo, 0, len(cat.DeviceList.Items))
		for _, it := range cat.DeviceList.Items {
			out = append(out, ChannelInfo{
				DeviceID:     it.DeviceID,
				Name:         it.Name,
				Status:       it.Status,
				Manufacturer: it.Manufacturer,
				Model:        it.Model,
			})
		}
		s.Registry.UpdateChannels(deviceID, out)
		return out, nil
	case <-ctx.Done():
		// 超时也返回缓存（若有）。
		if d, ok := s.Registry.Find(deviceID); ok && len(d.Channels) > 0 {
			return d.Channels, nil
		}
		return nil, fmt.Errorf("catalog query timeout for %s", deviceID)
	}
}

// QueryDeviceInfo 查询设备信息并等待应答。
func (s *Service) QueryDeviceInfo(ctx context.Context, deviceID string) (*manscdp.DeviceInfoResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	ch, err := s.sendQuery(ctx, deviceID, "DeviceInfo")
	if err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		info, ok := resp.(manscdp.DeviceInfoResponse)
		if !ok {
			return nil, fmt.Errorf("unexpected deviceinfo response type")
		}
		return &info, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("deviceinfo query timeout for %s", deviceID)
	}
}

// SendPTZ 向通道下发云台控制指令（action ∈ left/right/up/down/zoom_in/zoom_out/stop）。
func (s *Service) SendPTZ(ctx context.Context, channelID, action string, speed int) error {
	dev, _, ok := s.Registry.FindByChannel(channelID)
	if !ok {
		return fmt.Errorf("channel %s not found in any device catalog", channelID)
	}
	var ptz gbcode.PTZ
	switch action {
	case "left":
		ptz.Pan = -1
	case "right":
		ptz.Pan = 1
	case "up":
		ptz.Tilt = 1
	case "down":
		ptz.Tilt = -1
	case "zoom_in":
		ptz.Zoom = 1
	case "zoom_out":
		ptz.Zoom = -1
	case "stop":
	default:
		return fmt.Errorf("unknown ptz action %q", action)
	}
	if speed <= 0 || speed > 255 {
		speed = 80
	}
	ptz.PanSpeed, ptz.TiltSpeed = speed, speed
	ptz.ZoomSpeed = 5
	body := manscdp.BuildDeviceControlPTZ(s.nextSN(), channelID, gbcode.EncodePTZCmd(ptz))
	s.logger.Info("uplevel ptz control", "channel", channelID, "action", action, "speed", speed)
	return s.sendMessage(ctx, dev.DeviceID, body)
}
