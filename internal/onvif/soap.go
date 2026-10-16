package onvif

import (
	"fmt"
	"net/http"
	"regexp"
)

// parseSOAPAction 提取 SOAP Body 里的请求动作名与该元素片段。
// ONVIF 请求形如 <s:Envelope ...><s:Body><ns:GetDeviceInformation ...>...</ns:GetDeviceInformation></s:Body></s:Envelope>。
var bodyInnerRe = regexp.MustCompile(`(?is)<(?:\w+:)?Body[^>]*>(.*)</(?:\w+:)?Body>`)
var firstElemRe = regexp.MustCompile(`(?is)<(\w+):(\w+)[\s/>]`)

// parseSOAPAction 返回动作名（Body 首子元素本地名）与 Body 内部原文。
func parseSOAPAction(body string) (action string, payload string) {
	m := bodyInnerRe.FindStringSubmatch(body)
	if m == nil {
		return "", ""
	}
	inner := m[1]
	e := firstElemRe.FindStringSubmatch(inner)
	if e == nil {
		return "", inner
	}
	return e[2], inner
}

// writeSOAPFault 输出 SOAP 1.2 Fault。
func writeSOAPFault(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", `application/soap+xml; charset=utf-8`)
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(soapEnvelope(fmt.Sprintf(`
<env:Fault>
  <env:Code><env:Value>env:Sender</env:Value></env:Code>
  <env:Reason><env:Text xml:lang="en">%s</env:Text></env:Reason>
</env:Fault>`, xmlEscape(reason)))))
}

// soapEnvelope 包裹 SOAP 1.2 信封（ONVIF 标准 namespace）。
func soapEnvelope(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope"
  xmlns:tt="http://www.onvif.org/ver10/schema"
  xmlns:tds="http://www.onvif.org/ver10/device/wsdl"
  xmlns:trt="http://www.onvif.org/ver10/media/wsdl"
  xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"
  xmlns:xs="http://www.w3.org/2001/XMLSchema">` + "\n<env:Body>" + body + "\n</env:Body></env:Envelope>"
}
