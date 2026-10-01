// Package manscdp 实现 GB/T 28181 MANSCDP 协议的 XML 消息构造与解析。
// 覆盖设备端最小集：Keepalive 上报、Catalog/DeviceInfo 应答、DeviceControl 解析。
package manscdp

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// Query 平台 → 设备的查询/控制消息（同一结构按 CmdType 区分）。
// 根元素不限定（Query/Control/Notify 均可出现），按字段名解析。
type Query struct {
	CmdType  string    `xml:"CmdType"`
	SN       int       `xml:"SN"`
	DeviceID string    `xml:"DeviceID"`
	PTZCmd   string    `xml:"PTZCmd"`
	Info     *CtrlInfo `xml:"Info,omitempty"`
}

// CtrlInfo DeviceControl 的扩展信息。
type CtrlInfo struct {
	ControlPriority int `xml:"ControlPriority"`
}

// Channel 目录通道项。
type Channel struct {
	DeviceID     string `xml:"DeviceID"`
	Name         string `xml:"Name"`
	Manufacturer string `xml:"Manufacturer"`
	Model        string `xml:"Model"`
	Owner        string `xml:"Owner"`
	CivilCode   string `xml:"CivilCode"`
	Address     string `xml:"Address"`
	Parental    int    `xml:"Parental"`
	ParentID    string `xml:"ParentID"`
	SafetyWay   int    `xml:"SafetyWay"`
	RegisterWay int    `xml:"RegisterWay"`
	Secrecy     int    `xml:"Secrecy"`
	Status      string `xml:"Status"` // ON / OFF
}

// CatalogResponse 目录查询应答。
type CatalogResponse struct {
	XMLName    xml.Name  `xml:"Response"`
	CmdType    string    `xml:"CmdType"`
	SN         int       `xml:"SN"`
	DeviceID   string    `xml:"DeviceID"`
	SumNum     int       `xml:"SumNum"`
	DeviceList DeviceList `xml:"DeviceList"`
}

// DeviceList 通道列表（Num 属性）。
type DeviceList struct {
	Num   int       `xml:"Num,attr"`
	Items []Channel `xml:"Item"`
}

// DeviceInfoResponse 设备信息查询应答。
type DeviceInfoResponse struct {
	XMLName      xml.Name `xml:"Response"`
	CmdType      string   `xml:"CmdType"`
	SN           int      `xml:"SN"`
	DeviceID     string   `xml:"DeviceID"`
	DeviceName   string   `xml:"DeviceName"`
	Manufacturer string   `xml:"Manufacturer"`
	Model        string   `xml:"Model"`
	Firmware     string   `xml:"Firmware"`
	ChannelCount int      `xml:"ChannelSum"`
	Result       string   `xml:"Result"`
}

// Keepalive 心跳上报。
type Keepalive struct {
	XMLName  xml.Name `xml:"Notify"`
	CmdType  string   `xml:"CmdType"`
	SN       int      `xml:"SN"`
	DeviceID string   `xml:"DeviceID"`
	Status   string   `xml:"Status"`
}

// ParseQuery 解析平台发来的 Query 消息（Catalog/DeviceInfo/DeviceControl 等）。
func ParseQuery(body []byte) (Query, error) {
	var q Query
	// 平台消息以 <?xml ...?> 声明开头，encoding 可能为 GB2312；
	// 声明交给标准库按 UTF-8 容错处理。
	if err := xml.Unmarshal(body, &q); err != nil {
		// 尝试剥离 XML 声明（个别平台声明声明 GB2312 但实际内容为 UTF-8）。
		if idx := strings.Index(string(body), "?>"); idx >= 0 {
			if err2 := xml.Unmarshal([]byte(strings.TrimSpace(string(body[idx+2:]))), &q); err2 == nil {
				return q, nil
			}
		}
		return q, fmt.Errorf("parse manscdp query: %w", err)
	}
	return q, nil
}

// BuildKeepalive 构造心跳 XML。
func BuildKeepalive(sn int, deviceID string) string {
	k := Keepalive{CmdType: "Keepalive", SN: sn, DeviceID: deviceID, Status: "OK"}
	return marshal(k)
}

// BuildCatalog 构造目录应答 XML。
func BuildCatalog(sn int, deviceID string, channels []Channel) string {
	resp := CatalogResponse{
		CmdType:    "Catalog",
		SN:         sn,
		DeviceID:   deviceID,
		SumNum:     len(channels),
		DeviceList: DeviceList{Num: len(channels), Items: channels},
	}
	return marshal(resp)
}

// BuildDeviceInfo 构造设备信息应答 XML。
func BuildDeviceInfo(sn int, deviceID, name, manufacturer, model, firmware string, channelCount int) string {
	resp := DeviceInfoResponse{
		CmdType:      "DeviceInfo",
		SN:           sn,
		DeviceID:     deviceID,
		DeviceName:   name,
		Manufacturer: manufacturer,
		Model:        model,
		Firmware:     firmware,
		ChannelCount: channelCount,
		Result:       "OK",
	}
	return marshal(resp)
}

func marshal(v any) string {
	data, err := xml.MarshalIndent(v, "", "\r\n")
	if err != nil {
		// 结构体均为简单字段，marshal 不应失败。
		return ""
	}
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\r\n")
	sb.Write(data)
	return sb.String()
}

// SNString 序列号字符串形式（拼消息时使用）。
func SNString(sn int) string { return strconv.Itoa(sn) }
