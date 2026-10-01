package psrtp

import (
	"encoding/binary"
	"net"
	"testing"
)

// collectUDP 在随机端口上收包，返回回调。
func startUDPReceiver(t *testing.T) (*net.UDPAddr, chan []byte) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	ch := make(chan []byte, 64)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			ch <- pkt
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return conn.LocalAddr().(*net.UDPAddr), ch
}

func TestUDPSenderFragmentation(t *testing.T) {
	addr, ch := startUDPReceiver(t)
	s, err := NewUDPSender(addr.String(), 0x05F5E101) // y=0100000001 → 100000001
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	defer s.Close()

	// 3000 字节 PS → 3 个 RTP 片（1400+1400+200）
	ps := make([]byte, 3000)
	for i := range ps {
		ps[i] = byte(i)
	}
	if err := s.SendPS(ps, 123456); err != nil {
		t.Fatalf("send: %v", err)
	}

	var pkts [][]byte
	for i := 0; i < 3; i++ {
		p := <-ch
		pkts = append(pkts, p)
	}
	// 校验 RTP 头
	for i, p := range pkts {
		if p[0] != 0x80 {
			t.Errorf("pkt%d version", i)
		}
		pt := p[1] & 0x7F
		m := p[1] >> 7
		if pt != 96 {
			t.Errorf("pkt%d pt=%d", i, pt)
		}
		if i < 2 && m != 0 {
			t.Errorf("pkt%d marker should be 0", i)
		}
		if i == 2 && m != 1 {
			t.Errorf("last pkt marker should be 1")
		}
		ts := binary.BigEndian.Uint32(p[4:8])
		if ts != 123456 {
			t.Errorf("pkt%d ts=%d", i, ts)
		}
		ssrc := binary.BigEndian.Uint32(p[8:12])
		if ssrc != 0x05F5E101 {
			t.Errorf("pkt%d ssrc=%08X", i, ssrc)
		}
	}
	// seq 连续
	seq0 := binary.BigEndian.Uint16(pkts[0][2:4])
	for i := 1; i < 3; i++ {
		if got := binary.BigEndian.Uint16(pkts[i][2:4]); got != seq0+uint16(i) {
			t.Errorf("pkt%d seq=%d want %d", i, got, seq0+uint16(i))
		}
	}
	// 载荷拼回原文
	total := append(append([]byte{}, pkts[0][12:]...), pkts[1][12:]...)
	total = append(total, pkts[2][12:]...)
	if len(total) != 3000 {
		t.Fatalf("payload total = %d", len(total))
	}
	for i := range ps {
		if total[i] != ps[i] {
			t.Fatalf("payload mismatch at %d", i)
		}
	}
}

func TestTCPSenderRFC4571(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	type result struct {
		data []byte
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			resCh <- result{err: err}
			return
		}
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		resCh <- result{data: buf[:n], err: err}
	}()

	s, err := NewTCPSender(ln.Addr().String(), 42)
	if err != nil {
		t.Fatalf("new tcp sender: %v", err)
	}
	defer s.Close()
	ps := make([]byte, 100)
	if err := s.SendPS(ps, 7); err != nil {
		t.Fatalf("send: %v", err)
	}
	res := <-resCh
	if res.err != nil {
		t.Fatalf("receiver: %v", res.err)
	}
	data := res.data
	if len(data) != 2+RTPHeaderSize+100 {
		t.Fatalf("framed length = %d", len(data))
	}
	size := binary.BigEndian.Uint16(data[:2])
	if int(size) != RTPHeaderSize+100 {
		t.Errorf("rfc4571 length prefix = %d", size)
	}
	if data[2] != 0x80 || data[3]&0x7F != 96 {
		t.Errorf("rtp header: % X", data[2:4])
	}
	if binary.BigEndian.Uint32(data[6:10]) != 7 {
		t.Errorf("ts = %d", binary.BigEndian.Uint32(data[6:10]))
	}
	if binary.BigEndian.Uint32(data[10:14]) != 42 {
		t.Errorf("ssrc = %d", binary.BigEndian.Uint32(data[10:14]))
	}
}

func TestSenderCloseIdempotent(t *testing.T) {
	s, err := NewUDPSender("127.0.0.1:1", 1)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("close again: %v", err)
	}
	if err := s.SendPS([]byte{1}, 1); err == nil {
		t.Error("send after close should fail")
	}
}
