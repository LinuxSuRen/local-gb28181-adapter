package sig

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"

	"github.com/emiago/sipgo/sip"
	"github.com/linuxsuren/local-gb28181-adapter/internal/config"
	"github.com/linuxsuren/local-gb28181-adapter/internal/gbcode"
	"github.com/linuxsuren/local-gb28181-adapter/internal/gbsdp"
	"github.com/linuxsuren/local-gb28181-adapter/internal/manscdp"
	"github.com/linuxsuren/local-gb28181-adapter/internal/psrtp"
)

// ---- MESSAGE：目录查询 / 设备信息查询 / 云台控制 ----

func (s *Service) onMessage(req *sip.Request, tx sip.ServerTransaction) {
	q, err := manscdp.ParseQuery(req.Body())
	if err != nil {
		s.logger.Warn("gb28181 bad message body", "err", err.Error())
		_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Message", nil))
		return
	}
	// 先应答 200，再异步执行查询响应（应答本身也是一条新 MESSAGE）。
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))

	root := s.store.Root()
	switch q.CmdType {
	case "Catalog":
		go s.respondCatalog(q)
	case "DeviceInfo":
		go s.respondDeviceInfo(q)
	case "DeviceControl":
		s.handleDeviceControl(root, q)
	default:
		s.logger.Debug("gb28181 message ignored", "cmd", q.CmdType, "sn", q.SN)
	}
}

// respondCatalog 应答目录查询：每个启用的摄像头一个国标通道。
func (s *Service) respondCatalog(q manscdp.Query) {
	root := s.store.Root()
	p := root.Platform
	channels := make([]manscdp.Channel, 0, len(root.Cameras))
	for _, cam := range root.Cameras {
		if !cam.Enabled {
			continue
		}
		name := cam.Name
		if cam.Infrared && !strings.Contains(name, "infrared") {
			name = name + " infrared"
		}
		status := "OFF"
		if st, ok := s.streams.Status(cam.ID); ok && st.Running {
			status = "ON"
		}
		channels = append(channels, manscdp.Channel{
			DeviceID:     cam.GBChannelID(p.DeviceID),
			Name:         name,
			Manufacturer: root.Manufacturer,
			Model:        root.Model,
			Owner:        root.Manufacturer,
			CivilCode:    p.DeviceID[:8],
			Address:      name,
			Parental:     0,
			ParentID:     p.DeviceID,
			SafetyWay:    0,
			RegisterWay:  1,
			Secrecy:      0,
			Status:       status,
		})
	}
	body := manscdp.BuildCatalog(q.SN, p.DeviceID, channels)
	go func() {
		if ctx := s.queryCtx(); ctx != nil {
			if err := s.sendMessage(ctx, body); err != nil {
				s.logger.Warn("gb28181 catalog respond failed", "err", err.Error())
			}
		}
	}()
}

// respondDeviceInfo 应答设备信息查询。
func (s *Service) respondDeviceInfo(q manscdp.Query) {
	root := s.store.Root()
	p := root.Platform
	enabled := 0
	for _, cam := range root.Cameras {
		if cam.Enabled {
			enabled++
		}
	}
	body := manscdp.BuildDeviceInfo(q.SN, p.DeviceID, root.Model, root.Manufacturer, root.Model, root.Firmware, enabled)
	go func() {
		if ctx := s.queryCtx(); ctx != nil {
			if err := s.sendMessage(ctx, body); err != nil {
				s.logger.Warn("gb28181 deviceinfo respond failed", "err", err.Error())
			}
		}
	}()
}

// handleDeviceControl 处理云台控制（映射到虚拟云台状态机）。
func (s *Service) handleDeviceControl(root config.Root, q manscdp.Query) {
	if q.PTZCmd == "" {
		s.logger.Debug("gb28181 device control without ptz cmd", "sn", q.SN)
		return
	}
	cam, ok := root.FindCameraByChannel(q.DeviceID, root.Platform.DeviceID)
	if !ok {
		s.logger.Warn("gb28181 device control for unknown channel", "channel", q.DeviceID)
		return
	}
	ptz, err := gbcode.ParsePTZCmd(q.PTZCmd)
	if err != nil {
		// 预置位等扩展指令：确认收到但本地不执行。
		s.logger.Debug("gb28181 ptz cmd skipped", "camera", cam.ID, "err", err.Error())
		return
	}
	node := s.ptz.Get(cam.ID)
	speed := ptz.PanSpeed
	if speed == 0 {
		speed = ptz.TiltSpeed
	}
	if speed == 0 {
		speed = 50
	}
	norm := func(v int, max int) float64 {
		return float64(v) / float64(max)
	}
	switch {
	case ptz.Pan != 0 || ptz.Tilt != 0:
		dir := 1.0
		if ptz.Pan < 0 || ptz.Tilt < 0 {
			dir = -1.0
		}
		if ptz.Pan != 0 {
			node.ContinuousMove(dir*norm(speed, 255), 0, 0)
		} else {
			node.ContinuousMove(0, dir*norm(speed, 255), 0)
		}
	case ptz.Zoom != 0:
		dir := 1.0
		if ptz.Zoom < 0 {
			dir = -1.0
		}
		zoomSpeed := ptz.ZoomSpeed
		if zoomSpeed == 0 {
			zoomSpeed = 5
		}
		node.ContinuousMove(0, 0, dir*norm(zoomSpeed, 15))
	default:
		node.Stop(true, true)
	}
	s.logger.Info("gb28181 ptz control", "camera", cam.ID,
		"pan", ptz.Pan, "tilt", ptz.Tilt, "zoom", ptz.Zoom, "speed", speed)
}

// ---- INVITE / ACK / BYE：点播会话 ----

func (s *Service) onInvite(req *sip.Request, tx sip.ServerTransaction) {
	root := s.store.Root()
	p := root.Platform

	// 目标通道：Request-URI 用户段，兜底 To 头。
	channelID := req.Recipient.User
	if channelID == "" && req.To() != nil {
		channelID = req.To().Address.User
	}
	cam, ok := root.FindCameraByChannel(channelID, p.DeviceID)
	if !ok || !cam.Enabled {
		s.logger.Warn("gb28181 invite for unknown/disabled channel", "channel", channelID)
		_ = tx.Respond(sip.NewResponseFromRequest(req, 404, "Not Found", nil))
		return
	}

	offer, err := gbsdp.ParseOffer(string(req.Body()))
	if err != nil {
		s.logger.Warn("gb28181 invite sdp rejected", "err", err.Error(), "channel", channelID)
		_ = tx.Respond(sip.NewResponseFromRequest(req, 488, "Not Acceptable Here", nil))
		return
	}
	if offer.Session != "" && offer.Session != "Play" {
		// 仅实时点播；回放/下载不支持。
		s.logger.Warn("gb28181 invite session unsupported", "s", offer.Session)
		_ = tx.Respond(sip.NewResponseFromRequest(req, 488, "Not Acceptable Here", nil))
		return
	}

	ssrc := offer.SSRC
	if ssrc == 0 {
		ssrc = randomSSRC()
	}

	// 建立媒体发送器。
	dst := net.JoinHostPort(offer.IP, strconv.Itoa(offer.Port))
	var (
		sender *psrtp.Sender
	)
	switch offer.Transport {
	case gbsdp.TransportUDP:
		sender, err = psrtp.NewUDPSender(dst, ssrc)
	default:
		sender, err = psrtp.NewTCPSender(dst, ssrc)
	}
	if err != nil {
		s.logger.Error("gb28181 media sender failed", "err", err.Error(), "dst", dst)
		_ = tx.Respond(sip.NewResponseFromRequest(req, 500, "Media Error", nil))
		return
	}

	// 建立对话（补 To tag）。
	dlg, err := s.dialogUA.ReadInvite(req, tx)
	if err != nil {
		_ = sender.Close()
		s.logger.Warn("gb28181 invite dialog failed", "err", err.Error())
		_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
		return
	}

	sess := newSession(s, dlg, cam, offer, sender, channelID)
	s.addSession(dlg.ID, sess)

	// 100 Trying（非必须，但帮助平台侧等待）。
	_ = tx.Respond(sip.NewResponseFromRequest(req, 100, "Trying", nil))
	answer := gbsdp.BuildAnswer(channelID, s.AdvertiseIP, sender.LocalPort(), offer.Transport, ssrc)
	res := sip.NewResponseFromRequest(dlg.InviteRequest, 200, "OK", []byte(answer))
	// Contact 身份使用被点播的通道编码（GB28181 设备端惯例）。
	res.AppendHeader(sip.NewHeader("Contact",
		fmt.Sprintf("<sip:%s@%s>", channelID, s.contactHost())))
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	if err := tx.Respond(res); err != nil {
		_ = sender.Close()
		s.takeSession(dlg.ID)
		s.logger.Error("gb28181 invite respond failed", "err", err.Error())
		return
	}
	// INVITE 事务被取消/终止且未确认时回收会话。
	tx.OnTerminate(func(string, error) {
		if !sess.confirmed.Load() {
			s.takeSession(dlg.ID)
			sess.close("invite transaction terminated")
		}
	})
	s.logger.Info("gb28181 invite answered",
		"channel", channelID,
		"camera", cam.ID,
		"transport", string(offer.Transport),
		"remote", dst,
		"ssrc", offer.SSRCString,
		"dialog", dlg.ID)
}

func (s *Service) onAck(req *sip.Request, _ sip.ServerTransaction) {
	id, err := sip.DialogIDFromRequestUAS(req)
	if err != nil {
		return
	}
	sess := s.getSession(id)
	if sess == nil {
		s.logger.Debug("gb28181 ack for unknown dialog", "dialog", id)
		return
	}
	if !sess.confirmed.Swap(true) {
		go sess.start()
	}
}

func (s *Service) onBye(req *sip.Request, tx sip.ServerTransaction) {
	id, err := sip.DialogIDFromRequestUAS(req)
	if err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		return
	}
	sess := s.takeSession(id)
	if sess == nil {
		// 未知对话也回 200，避免平台重发。
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		return
	}
	if err := sess.dlg.ReadBye(req, tx); err != nil {
		// CSeq 校验失败等场景兜底应答。
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	}
	sess.close("bye")
	s.logger.Info("gb28181 bye", "dialog", id, "channel", sess.channelID)
}

// randomSSRC 生成 9 位十进制 SSRC（GB28181 y= 行习惯为十进制）。
func randomSSRC() uint32 {
	n, err := rand.Int(rand.Reader, big.NewInt(899999999))
	if err != nil {
		var b [4]byte
		_, _ = rand.Read(b[:])
		return 100000000 + binary.BigEndian.Uint32(b[:])%899999999
	}
	return uint32(n.Int64() + 100000000)
}
