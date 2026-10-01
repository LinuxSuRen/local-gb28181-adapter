package gbsdp

import (
	"errors"
	"strings"
	"testing"
)

func TestParseOfferUDP(t *testing.T) {
	body := "v=0\r\n" +
		"o=34020000001320000001 0 0 IN IP4 192.168.1.10\r\n" +
		"s=Play\r\n" +
		"c=IN IP4 192.168.1.10\r\n" +
		"t=0 0\r\n" +
		"m=video 30000 RTP/AVP 96 98 97\r\n" +
		"a=recvonly\r\n" +
		"y=0100000001\r\n"
	o, err := ParseOffer(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.ChannelID != "34020000001320000001" || o.IP != "192.168.1.10" || o.Port != 30000 {
		t.Errorf("got %+v", o)
	}
	if o.Transport != TransportUDP {
		t.Errorf("transport = %v", o.Transport)
	}
	if o.SSRC != 100000001 || o.SSRCString != "0100000001" {
		t.Errorf("ssrc = %d %q", o.SSRC, o.SSRCString)
	}
}

func TestParseOfferTCPPassive(t *testing.T) {
	body := "v=0\r\n" +
		"o=34020000001320000001 0 0 IN IP4 192.168.1.10\r\n" +
		"s=Play\r\n" +
		"c=IN IP4 192.168.1.10\r\n" +
		"t=0 0\r\n" +
		"m=video 30000 TCP/RTP/AVP 96 98 97\r\n" +
		"a=setup:passive\r\n" +
		"a=connection:new\r\n" +
		"y=0100000001\r\n"
	o, err := ParseOffer(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.Transport != TransportTCPPassive {
		t.Errorf("transport = %v", o.Transport)
	}
}

func TestParseOfferTCPActiveUnsupported(t *testing.T) {
	body := "v=0\r\n" +
		"o=ch 0 0 IN IP4 192.168.1.10\r\n" +
		"c=IN IP4 192.168.1.10\r\n" +
		"m=video 30000 TCP/RTP/AVP 96\r\n" +
		"a=setup:active\r\n" +
		"y=0100000001\r\n"
	if _, err := ParseOffer(body); !errors.Is(err, ErrUnsupportedTransport) {
		t.Errorf("err = %v", err)
	}
}

func TestParseOfferErrors(t *testing.T) {
	if _, err := ParseOffer("v=0\r\nm=video 0 RTP/AVP 96\r\n"); err == nil {
		t.Error("missing c= should fail")
	}
	if _, err := ParseOffer("v=0\r\nc=IN IP4 1.2.3.4\r\n"); err == nil {
		t.Error("missing m= should fail")
	}
}

func TestBuildAnswerUDP(t *testing.T) {
	sdp := BuildAnswer("34020000001320000001", "192.168.1.20", 5060, TransportUDP, 100000001)
	for _, want := range []string{
		"o=34020000001320000001 0 0 IN IP4 192.168.1.20",
		"s=Play",
		"c=IN IP4 192.168.1.20",
		"m=video 5060 RTP/AVP 96",
		"a=sendonly",
		"y=100000001",
	} {
		if !strings.Contains(sdp, want) {
			t.Errorf("answer missing %q:\n%s", want, sdp)
		}
	}
	if strings.Contains(sdp, "a=setup") {
		t.Error("udp answer should not contain setup")
	}
}

func TestBuildAnswerTCPPassive(t *testing.T) {
	sdp := BuildAnswer("ch", "1.1.1.1", 30000, TransportTCPPassive, 42)
	if !strings.Contains(sdp, "m=video 30000 TCP/RTP/AVP 96") {
		t.Errorf("answer missing TCP m line:\n%s", sdp)
	}
	if !strings.Contains(sdp, "a=setup:active") {
		t.Errorf("answer should mirror active:\n%s", sdp)
	}
	if !strings.Contains(sdp, "y=42") {
		t.Errorf("answer missing y= line:\n%s", sdp)
	}
}
