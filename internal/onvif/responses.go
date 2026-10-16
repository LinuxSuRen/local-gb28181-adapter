package onvif

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// respGetSystemDateAndTime 设备时间（部分客户端握手首个请求）。
var respGetSystemDateAndTime = soapEnvelope(`
<tds:GetSystemDateAndTimeResponse>
  <tds:SystemDateAndTime>
    <tt:DateTimeType>NTP</tt:DateTimeType>
    <tt:DaylightSavings>false</tt:DaylightSavings>
    <tt:UTCDateTime>
      <tt:Time><tt:Hour>{{H}}</tt:Hour><tt:Minute>{{M}}</tt:Minute><tt:Second>{{S}}</tt:Second></tt:Time>
      <tt:Date><tt:Year>{{Y}}</tt:Year><tt:Month>{{MO}}</tt:Month><tt:Day>{{D}}</tt:Day></tt:Date>
    </tt:UTCDateTime>
  </tds:SystemDateAndTime>
</tds:GetSystemDateAndTimeResponse>`)

// respGetCapabilities 能力声明：Device/Media/PTZ 服务地址。
func (s *Server) respGetCapabilities(req svcReq) string {
	svc := s.serviceURL(req.id)
	return soapEnvelope(fmt.Sprintf(`
<tds:GetCapabilitiesResponse>
  <tds:Capabilities>
    <tt:Device>
      <tt:XAddr>%s</tt:XAddr>
    </tt:Device>
    <tt:Media>
      <tt:XAddr>%s</tt:XAddr>
      <tt:StreamingCapabilities><tt:RTPMulticast>false</tt:RTPMulticast><tt:RTP_TCP>true</tt:RTP_TCP><tt:RTP_UDP>true</tt:RTP_UDP></tt:StreamingCapabilities>
    </tt:Media>
    <tt:PTZ>
      <tt:XAddr>%s</tt:XAddr>
    </tt:PTZ>
  </tds:Capabilities>
</tds:GetCapabilitiesResponse>`, svc, svc, svc))
}

// respGetServices 服务列表。
func (s *Server) respGetServices(req svcReq) string {
	svc := s.serviceURL(req.id)
	return soapEnvelope(fmt.Sprintf(`
<tds:GetServicesResponse>
  <tds:Service>
    <tds:Namespace>http://www.onvif.org/ver10/device/wsdl</tds:Namespace>
    <tds:XAddr>%s</tds:XAddr>
    <tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version>
  </tds:Service>
  <tds:Service>
    <tds:Namespace>http://www.onvif.org/ver10/media/wsdl</tds:Namespace>
    <tds:XAddr>%s</tds:XAddr>
    <tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version>
  </tds:Service>
  <tds:Service>
    <tds:Namespace>http://www.onvif.org/ver20/ptz/wsdl</tds:Namespace>
    <tds:XAddr>%s</tds:XAddr>
    <tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version>
  </tds:Service>
</tds:GetServicesResponse>`, svc, svc, svc))
}

// respGetDeviceInformation 返回国标设备信息（型号厂商固件从 GB 目录透传）。
func (s *Server) respGetDeviceInformation(req svcReq) string {
	dev, ok := s.up.Registry.Find(req.deviceID)
	manu, model := "", ""
	if ok && dev.Info != nil {
		manu = dev.Info.Manufacturer
		model = dev.Info.Model
	}
	if !ok {
		if ch, _, ok2 := s.channelInfo(req.id); ok2 {
			manu, model = ch.Manufacturer, ch.Model
		}
	}
	if manu == "" {
		manu = "GB28181"
	}
	if model == "" {
		model = "GB28181 Camera"
	}
	firmware := ""
	if ok && dev.Info != nil {
		firmware = dev.Info.Firmware
	}
	return soapEnvelope(fmt.Sprintf(`
<tds:GetDeviceInformationResponse>
  <tds:Manufacturer>%s</tds:Manufacturer>
  <tds:Model>%s</tds:Model>
  <tds:FirmwareVersion>%s</tds:FirmwareVersion>
  <tds:SerialNumber>%s</tds:SerialNumber>
  <tds:HardwareId>%s</tds:HardwareId>
</tds:GetDeviceInformationResponse>`,
		xmlEscape(manu), xmlEscape(model), xmlEscape(firmware),
		xmlEscape(req.deviceID), xmlEscape(req.deviceID)))
}

// profileTokenPrefix ONVIF profile token 前缀（profile_N 按通道顺序）。
const profileTokenPrefix = "profile_"

// respGetProfiles 每通道一个 profile（profile_1..N，通常 profile_1 可见光、
// 红外/热成像通道的名称带 infrared 后缀），一台相机对应多 profile。
func (s *Server) respGetProfiles(req svcReq) string {
	up := s.store.Root().Uplevel
	var b strings.Builder
	for i, ch := range req.channels {
		name := fmt.Sprintf("GB28181_Main_%s", shortID(ch.DeviceID))
		if up.IsInfraredChannel(ch.DeviceID, ch.Name) {
			name += "_infrared"
		}
		fmt.Fprintf(&b, `
  <trt:Profiles token="%s%d" fixed="true">
    <tt:Name>%s</tt:Name>
    <tt:VideoSourceConfiguration token="vsrc_%d">
      <tt:Name>VideoSource_%d</tt:Name>
      <tt:UseCount>1</tt:UseCount>
      <tt:SourceToken>video_source_%d</tt:SourceToken>
      <tt:Bounds x="0" y="0" width="1920" height="1080"/>
    </tt:VideoSourceConfiguration>
    <tt:VideoEncoderConfiguration token="vec_%d">
      <tt:Name>VideoEncoder_%d</tt:Name>
      <tt:UseCount>1</tt:UseCount>
      <tt:Encoding>H264</tt:Encoding>
      <tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
      <tt:RateControl><tt:FrameRateLimit>25</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval></tt:RateControl>
    </tt:VideoEncoderConfiguration>
    <tt:PTZConfiguration token="ptz_1">
      <tt:Name>PTZ_1</tt:Name>
      <tt:UseCount>1</tt:UseCount>
      <tt:NodeToken>ptz_node_1</tt:NodeToken>
    </tt:PTZConfiguration>
  </trt:Profiles>`, profileTokenPrefix, i+1, xmlEscape(name), i+1, i+1, i+1, i+1, i+1)
	}
	return soapEnvelope("<trt:GetProfilesResponse>" + b.String() + "</trt:GetProfilesResponse>")
}

// shortID 编码缩略展示。
func shortID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

// respGetSnapshotUri 抓图地址（取第一通道；抓图跟随取流模式决策）。
func (s *Server) respGetSnapshotUri(req svcReq) string {
	channelID := req.id
	if len(req.channels) > 0 {
		channelID = req.channels[0].DeviceID
	}
	return soapEnvelope(fmt.Sprintf(`
<trt:GetSnapshotUriResponse>
  <trt:MediaUri>
    <tt:Uri>%s</tt:Uri>
    <tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>
    <tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>
    <tt:TimeToLive>3600</tt:TimeToLive>
  </trt:MediaUri>
</trt:GetSnapshotUriResponse>`, xmlEscape(s.snapshotURL(channelID))))
}

// respVideoConfigs 简化的编码配置（部分客户端取流前会请求）。
func (s *Server) respVideoConfigs(req svcReq) string {
	return soapEnvelope(`
<trt:GetVideoEncoderConfigurationsResponse>
  <trt:Configurations token="vec_1">
    <tt:Name>VideoEncoder_1</tt:Name>
    <tt:UseCount>1</tt:UseCount>
    <tt:Encoding>H264</tt:Encoding>
    <tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
    <tt:RateControl><tt:FrameRateLimit>25</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval></tt:RateControl>
    <tt:H264><tt:GovLength>50</tt:GovLength><tt:H264Profile>Main</tt:H264Profile></tt:H264>
    <tt:SessionTimeout>PT10S</tt:SessionTimeout>
  </trt:Configurations>
</trt:GetVideoEncoderConfigurationsResponse>`)
}

// respPTZStatus 云台状态（位置未知，返回静止）。
var respPTZStatus = soapEnvelope(`
<tptz:GetStatusResponse>
  <tptz:PTZStatus>
    <tt:Position>
      <tt:PanTilt x="0" y="0" space="http://www.onvif.org/ver10/schema/PositionPanTilt"/>
      <tt:Zoom x="0" space="http://www.onvif.org/ver10/schema/PositionZoom"/>
    </tt:Position>
    <tt:MoveStatus><tt:PanTilt>IDLE</tt:PanTilt></tt:MoveStatus>
    <tt:UTCTime>{{NOW}}</tt:UTCTime>
  </tptz:PTZStatus>
</tptz:GetStatusResponse>`)

// respPTZServiceCapabilities PTZ 能力简版。
var respPTZServiceCapabilities = soapEnvelope(`
<tptz:GetServiceCapabilitiesResponse>
  <tptz:Capabilities ContinuousMove="true" AbsoluteMove="false" RelativeMove="false"/>
</tptz:GetServiceCapabilitiesResponse>`)

// respPTZNodes PTZ 节点。
func (s *Server) respPTZNodes(req svcReq) string {
	return soapEnvelope(`
<tptz:GetNodesResponse>
  <tptz:PTZNode token="ptz_node_1">
    <tt:Name>PTZNode_1</tt:Name>
    <tt:SupportedPTZSpaces>
      <tt:AbsolutePanTiltPositionSpace><tt:URI>http://www.onvif.org/ver10/schema/PositionPanTilt</tt:URI></tt:AbsolutePanTiltPositionSpace>
      <tt:ContinuousPanTiltVelocitySpace><tt:URI>http://www.onvif.org/ver10/schema/VelocityPanTilt</tt:URI></tt:ContinuousPanTiltVelocitySpace>
    </tt:SupportedPTZSpaces>
  </tptz:PTZNode>
</tptz:GetNodesResponse>`)
}

// respPTZConfigurations PTZ 配置。
func (s *Server) respPTZConfigurations(req svcReq) string {
	return soapEnvelope(`
<tptz:GetConfigurationsResponse>
  <tptz:PTZConfiguration token="ptz_1">
    <tt:Name>PTZ_1</tt:Name>
    <tt:UseCount>1</tt:UseCount>
    <tt:NodeToken>ptz_node_1</tt:NodeToken>
  </tptz:PTZConfiguration>
</tptz:GetConfigurationsResponse>`)
}

// respGenericOK 未实现动作的通用应答（部分客户端要求非 Fault 才继续）。
var respGenericOK = soapEnvelope(`<tds:%sResponse/>`)

// ---- PTZ 动作 ----

// onvifVelocity ONVIF 速度/位置结构（忽略命名空间前缀，属性解析）。
type onvifVelocity struct {
	PanTilt struct {
		X float64 `xml:"x,attr"`
		Y float64 `xml:"y,attr"`
	} `xml:"PanTilt"`
	Zoom struct {
		X float64 `xml:"x,attr"`
	} `xml:"Zoom"`
}

// handleContinuousMove 解析 ONVIF 速度并翻译为国标云台指令。
// 用标准 XML 解析（任何命名空间前缀均兼容）；ProfileToken 决定目标通道。
func (s *Server) handleContinuousMove(req svcReq, payload string) string {
	var body struct {
		XMLName  xml.Name
		Velocity onvifVelocity `xml:"Velocity"`
	}
	if err := xml.Unmarshal([]byte(payload), &body); err != nil {
		s.logger.Warn("onvif ptz parse failed", "id", req.id, "err", err.Error())
		return soapEnvelope(`<tptz:ContinuousMoveResponse/>`)
	}
	token := soapQuery(payload, "ProfileToken")
	ch, ok := req.channelByToken(token)
	if !ok {
		s.logger.Warn("onvif ptz unknown profile", "id", req.id, "token", token)
		return soapEnvelope(`<tptz:ContinuousMoveResponse/>`)
	}
	action, speed := mapVelocity(body.Velocity.PanTilt.X, body.Velocity.PanTilt.Y, body.Velocity.Zoom.X)
	s.logger.Info("onvif ptz continuous move",
		"channel", ch.DeviceID,
		"pan", body.Velocity.PanTilt.X, "tilt", body.Velocity.PanTilt.Y, "zoom", body.Velocity.Zoom.X,
		"action", action)
	if action == "" {
		return soapEnvelope(`<tptz:ContinuousMoveResponse/>`)
	}
	ctx, cancel := contextTimeout(6 * time.Second)
	defer cancel()
	if err := s.up.SendPTZ(ctx, ch.DeviceID, action, speed); err != nil {
		s.logger.Warn("onvif ptz failed", "channel", ch.DeviceID, "action", action, "err", err.Error())
	}
	return soapEnvelope(`<tptz:ContinuousMoveResponse/>`)
}

// handlePTZStop 停止云台（ProfileToken 决定目标通道）。
func (s *Server) handlePTZStop(req svcReq, payload string) string {
	ch, ok := req.channelByToken(soapQuery(payload, "ProfileToken"))
	if !ok {
		return soapEnvelope(`<tptz:StopResponse/>`)
	}
	ctx, cancel := contextTimeout(6 * time.Second)
	defer cancel()
	if err := s.up.SendPTZ(ctx, ch.DeviceID, "stop", 0); err != nil {
		s.logger.Warn("onvif ptz stop failed", "channel", ch.DeviceID, "err", err.Error())
	}
	return soapEnvelope(`<tptz:StopResponse/>`)
}

// mapVelocity ONVIF 速度（-1..1 常见）→ 单一方向 action + 速度(0-255)。
// GB A50F 一次只表达一个方向，取主导轴。
func mapVelocity(pan, tilt, zoom float64) (action string, speed int) {
	speedOf := func(v float64) int {
		if v < 0 {
			v = -v
		}
		if v > 1 {
			v = 1
		}
		return 60 + int(v*180)
	}
	switch {
	case zoom != 0:
		if zoom > 0 {
			return "zoom_in", speedOf(zoom)
		}
		return "zoom_out", speedOf(zoom)
	case pan != 0 && abs(pan) >= abs(tilt):
		if pan > 0 {
			return "right", speedOf(pan)
		}
		return "left", speedOf(pan)
	case tilt != 0:
		if tilt > 0 {
			return "up", speedOf(tilt)
		}
		return "down", speedOf(tilt)
	}
	return "", 0
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
