package manscdp

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestParseQueryCatalog(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<Query>
<CmdType>Catalog</CmdType>
<SN>17060</SN>
<DeviceID>34020000002000000001</DeviceID>
</Query>`
	q, err := ParseQuery([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q.CmdType != "Catalog" || q.SN != 17060 || q.DeviceID != "34020000002000000001" {
		t.Errorf("got %+v", q)
	}
}

func TestParseQueryDeviceControl(t *testing.T) {
	body := `<?xml version="1.0" encoding="GB2312"?>
<Control>
<CmdType>DeviceControl</CmdType>
<SN>17</SN>
<DeviceID>34020000001320000001</DeviceID>
<PTZCmd>A50F0102050100B7</PTZCmd>
<Info><ControlPriority>5</ControlPriority></Info>
</Control>`
	// 根元素为 Control 而非 Query，仍应解析成功（字段名相同）。
	q, err := ParseQuery([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q.CmdType != "DeviceControl" || q.DeviceID != "34020000001320000001" {
		t.Errorf("got %+v", q)
	}
	if q.PTZCmd == "" {
		t.Error("PTZCmd should be parsed")
	}
}

func TestBuildAndParseCatalogRoundTrip(t *testing.T) {
	channels := []Channel{
		{DeviceID: "34020000001320000001", Name: "cam1", Manufacturer: "linuxsuren", Model: "local-gb28181-adapter", Status: "ON"},
		{DeviceID: "34020000001320000002", Name: "cam2", Status: "ON"},
	}
	xmlStr := BuildCatalog(12, "34020000002000000001", channels)
	if !strings.Contains(xmlStr, "<CmdType>Catalog</CmdType>") || !strings.Contains(xmlStr, "<SumNum>2</SumNum>") {
		t.Fatalf("unexpected xml: %s", xmlStr)
	}
	var resp CatalogResponse
	if err := unmarshalBody(xmlStr, &resp); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if resp.SN != 12 || resp.SumNum != 2 || resp.DeviceList.Num != 2 {
		t.Errorf("got %+v", resp)
	}
	if resp.DeviceList.Items[0].DeviceID != "34020000001320000001" || resp.DeviceList.Items[0].Status != "ON" {
		t.Errorf("item0: %+v", resp.DeviceList.Items[0])
	}
}

func TestBuildKeepalive(t *testing.T) {
	s := BuildKeepalive(5, "34020000002000000001")
	if !strings.Contains(s, "<CmdType>Keepalive</CmdType>") || !strings.Contains(s, "<Status>OK</Status>") {
		t.Errorf("keepalive xml: %s", s)
	}
}

func TestBuildDeviceInfo(t *testing.T) {
	s := BuildDeviceInfo(3, "34020000002000000001", "dev", "linuxsuren", "local-gb28181-adapter", "0.1.0", 2)
	var resp DeviceInfoResponse
	if err := unmarshalBody(s, &resp); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if resp.DeviceName != "dev" || resp.ChannelCount != 2 || resp.Result != "OK" {
		t.Errorf("got %+v", resp)
	}
}

func unmarshalBody(s string, v any) error {
	s = strings.TrimPrefix(s, `<?xml version="1.0" encoding="UTF-8"?>`)
	return xml.Unmarshal([]byte(strings.TrimSpace(s)), v)
}
