package psrtp

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// buildBoundedPES 构造显式 PES_packet_length 的 PES（DemuxPS 边界测试用），
// withPTS 控制是否携带 PTS，es 为裸 ES。
func buildBoundedPES(streamID byte, es []byte, pts int64, withPTS bool) []byte {
	var hdr []byte
	hdr = append(hdr, 0x80) // flags1：'10' 前缀
	if withPTS {
		hdr = append(hdr, 0x80, 5)           // flags2：PTS_DTS_flags='10'
		hdr = appendTimestamp(hdr, 0x2, pts) // header data：PTS 5 字节
	} else {
		hdr = append(hdr, 0x00, 0x00) // 无时间戳，header data 长度 0
	}
	out := []byte{0x00, 0x00, 0x01, streamID}
	out = binary.BigEndian.AppendUint16(out, uint16(len(hdr)+len(es)))
	out = append(out, hdr...)
	return append(out, es...)
}

// buildSysHeader 构造最小 system header（内容不严格，仅验证按长度域跳过）。
func buildSysHeader() []byte {
	body := []byte{0x12, 0x34, 0x56, 0x78}
	out := []byte{0x00, 0x00, 0x01, psSysHeaderID}
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)))
	return append(out, body...)
}

// annexB 拼接裸 NAL 为 AnnexB 码流（测试期望值）。
func annexB(nalus [][]byte) []byte {
	var out []byte
	for _, nalu := range nalus {
		out = append(out, annexBStart...)
		out = append(out, nalu...)
	}
	return out
}

func TestDemuxPSIDR(t *testing.T) {
	m := NewPSMuxer()
	nalus := [][]byte{
		[]byte{0x67, 0x64, 0x00, 0x1F},       // SPS
		[]byte{0x68, 0xEB, 0xE3},             // PPS
		[]byte{0x65, 0x88, 0x84, 0x21, 0xA0}, // IDR slice
	}
	ps := m.PackAU(nalus, 90000, 90000, true)
	video, pts, hasVideo := DemuxPS(ps)
	if !hasVideo {
		t.Fatal("IDR should carry video")
	}
	if want := annexB(nalus); !bytes.Equal(video, want) {
		t.Fatalf("ES mismatch\n got: % X\nwant: % X", video, want)
	}
	if pts != 90000 {
		t.Errorf("pts = %d, want 90000", pts)
	}
}

func TestDemuxPSWithDTS(t *testing.T) {
	m := NewPSMuxer()
	ps := m.PackAU([][]byte{{0x65, 0x01}}, 180000, 179700, false)
	_, pts, hasVideo := DemuxPS(ps)
	if !hasVideo {
		t.Fatal("expected video")
	}
	if pts != 180000 {
		t.Errorf("pts = %d, want 180000 (PTS 而非 DTS)", pts)
	}
}

func TestDemuxPSMultiPESBounded(t *testing.T) {
	es1 := append([]byte{}, annexBStart...)
	es1 = append(es1, 0x65, 1, 2, 3, 4)
	es2 := append([]byte{}, annexBStart...)
	es2 = append(es2, 0x41, 5, 6)

	var ps []byte
	ps = appendPackHeader(ps, 1000)
	ps = append(ps, buildSysHeader()...)
	ps = append(ps, buildBoundedPES(streamIDVideo, es1, 3600, true)...)
	ps = append(ps, buildBoundedPES(streamIDVideo, es2, 3600, false)...)
	ps = append(ps, 0x00, 0x00, 0x01, psProgramEndID)

	video, pts, hasVideo := DemuxPS(ps)
	if !hasVideo {
		t.Fatal("expected video")
	}
	want := append(append([]byte{}, es1...), es2...)
	if !bytes.Equal(video, want) {
		t.Fatalf("ES mismatch\n got: % X\nwant: % X", video, want)
	}
	if pts != 3600 {
		t.Errorf("pts = %d, want 3600", pts)
	}
}

func TestDemuxPSUnboundedPES(t *testing.T) {
	// 第一个 PES 不定长（PES_packet_length=0）：需扫描下一个 PS 层起始码定界，
	// ES 内的 AnnexB 起始码（00 00 01 + NAL 头 <= 0x7F）不得误判为边界。
	es1 := append([]byte{}, annexBStart...)
	es1 = append(es1, 0x65, 0xAA, 0xBB)
	es2 := append([]byte{}, annexBStart...)
	es2 = append(es2, 0x41, 0xCC)

	var pes1 []byte
	pes1 = append(pes1, 0x00, 0x00, 0x01, streamIDVideo, 0x00, 0x00) // 不定长
	pes1 = append(pes1, 0x80, 0x80, 5)
	pes1 = appendTimestamp(pes1, 0x2, 1800)
	pes1 = append(pes1, es1...)

	var ps []byte
	ps = appendPackHeader(ps, 500)
	ps = append(ps, pes1...)
	ps = append(ps, buildBoundedPES(streamIDVideo, es2, 1800, false)...)

	video, pts, hasVideo := DemuxPS(ps)
	if !hasVideo {
		t.Fatal("expected video")
	}
	want := append(append([]byte{}, es1...), es2...)
	if !bytes.Equal(video, want) {
		t.Fatalf("ES mismatch\n got: % X\nwant: % X", video, want)
	}
	if pts != 1800 {
		t.Errorf("pts = %d, want 1800", pts)
	}
}

func TestDemuxPSPackStuffing(t *testing.T) {
	var ps []byte
	ps = appendPackHeader(ps, 100)
	ps[13] = ps[13]&0xF8 | 0x03 // stuffing length = 3
	ps = append(ps, 0xFF, 0xFF, 0xFF)
	es := append([]byte{}, annexBStart...)
	es = append(es, 0x65, 0x77)
	ps = append(ps, buildBoundedPES(streamIDVideo, es, 1234, true)...)

	video, pts, hasVideo := DemuxPS(ps)
	if !hasVideo {
		t.Fatal("expected video after stuffing bytes")
	}
	if !bytes.Equal(video, es) {
		t.Fatalf("ES mismatch\n got: % X\nwant: % X", video, es)
	}
	if pts != 1234 {
		t.Errorf("pts = %d, want 1234", pts)
	}
}

func TestDemuxPSNoVideo(t *testing.T) {
	var ps []byte
	ps = appendPackHeader(ps, 800)
	ps = append(ps, buildBoundedPES(0xC0, []byte{0x11, 0x22, 0x33, 0x44}, 1800, true)...) // G711 音频
	ps = append(ps, buildBoundedPES(0xBD, []byte{0x55}, 1800, false)...)                  // 私有数据
	video, _, hasVideo := DemuxPS(ps)
	if hasVideo {
		t.Errorf("audio/private only should not yield video: % X", video)
	}
	if video != nil {
		t.Errorf("video should be nil, got % X", video)
	}
}

func TestDemuxPSGarbageNoPanic(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0x00, 0x00, 0x01},
		{0x00, 0x00, 0x01, psPackCode, 0x40}, // pack header 残缺
		{0x00, 0x00, 0x01, streamIDVideo, 0x00, 0x10, 0x80, 0x80}, // PES 头残缺
		{0xFF, 0xFF, 0x00, 0x00, 0x01, psPackCode},                // 前导垃圾
	}
	for i, ps := range cases {
		if video, _, hasVideo := DemuxPS(ps); hasVideo {
			t.Errorf("case %d: garbage should not yield video: % X", i, video)
		}
	}
}

// rxFrame 收流回调捕获的帧。
type rxFrame struct {
	data []byte
	pts  uint64
}

// expectFrame 等待并断言下一帧内容与 PTS。
func expectFrame(t *testing.T, ch <-chan rxFrame, want []byte, wantPTS uint64) {
	t.Helper()
	select {
	case f := <-ch:
		if !bytes.Equal(f.data, want) {
			t.Fatalf("frame mismatch\n got: % X\nwant: % X", f.data, want)
		}
		if f.pts != wantPTS {
			t.Fatalf("pts = %d, want %d", f.pts, wantPTS)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting frame")
	}
}

// startReceiver 启动收流器并把回调帧送入 channel。
func startReceiver(t *testing.T) (*UDPReceiver, chan rxFrame) {
	t.Helper()
	recv, err := NewUDPReceiver(0)
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}
	t.Cleanup(func() { _ = recv.Close() })
	if recv.LocalPort() <= 0 {
		t.Fatalf("random port not allocated: %d", recv.LocalPort())
	}
	ch := make(chan rxFrame, 16)
	recv.Start(func(frame []byte, pts uint64) {
		ch <- rxFrame{data: bytes.Clone(frame), pts: pts}
	})
	return recv, ch
}

// rtpSender 测试辅助：把 PS 帧按 MaxPacketSize 切片为 RTP 包发往收流端口。
// skip 为需要模拟丢弃的分片全局序号（从 0 计）。
type rtpSender struct {
	conn  *net.UDPConn
	seq   uint16
	index int
	skip  map[int]struct{}
}

func newRTPSender(t *testing.T, port int, firstSeq uint16, skip ...int) *rtpSender {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s := &rtpSender{conn: conn, seq: firstSeq, skip: map[int]struct{}{}}
	for _, i := range skip {
		s.skip[i] = struct{}{}
	}
	return s
}

// send 切片并发送一个 PS 帧；pt 为 RTP 载荷类型，末包置 marker。
func (s *rtpSender) send(t *testing.T, ps []byte, ts uint32, pt uint8) {
	t.Helper()
	maxPayload := MaxPacketSize - RTPHeaderSize
	for off := 0; off < len(ps); off += maxPayload {
		end := min(off+maxPayload, len(ps))
		if _, drop := s.skip[s.index]; !drop {
			pkt := rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         end == len(ps),
					PayloadType:    pt,
					SequenceNumber: s.seq,
					Timestamp:      ts,
					SSRC:           0x05F5E101,
				},
				Payload: ps[off:end],
			}
			raw, err := pkt.Marshal()
			if err != nil {
				t.Fatalf("marshal rtp: %v", err)
			}
			if _, err := s.conn.Write(raw); err != nil {
				t.Fatalf("write udp: %v", err)
			}
		}
		s.seq++
		s.index++
	}
}

func TestUDPReceiverLoopback(t *testing.T) {
	recv, ch := startReceiver(t)
	s := newRTPSender(t, recv.LocalPort(), 65533) // 从回绕点附近开始，覆盖 65535→0
	m := NewPSMuxer()

	// 帧 1：IDR（带 PSM），大 NAL 触发多 RTP 分片
	idr := [][]byte{{0x67, 0x64}, {0x68, 0xEB}, make([]byte, 3200)}
	s.send(t, m.PackAU(idr, 90000, 90000, true), 90000, DefaultPayloadType)
	// 帧 2：单包小 P 帧
	p := [][]byte{{0x41, 0x00, 0x01}}
	s.send(t, m.PackAU(p, 93600, 93600, false), 93600, DefaultPayloadType)

	expectFrame(t, ch, annexB(idr), 90000)
	expectFrame(t, ch, annexB(p), 93600)
}

func TestUDPReceiverLossAndRecover(t *testing.T) {
	recv, ch := startReceiver(t)
	// 大帧分 3+ 片，故意丢掉中间一片（全局分片序号 1）
	s := newRTPSender(t, recv.LocalPort(), 100, 1)
	m := NewPSMuxer()

	big := [][]byte{make([]byte, 4000)}
	s.send(t, m.PackAU(big, 90000, 90000, false), 90000, DefaultPayloadType)
	// 后续完整帧必须精确恢复（PS 自同步重新对齐）
	next := [][]byte{{0x41, 0x07}}
	s.send(t, m.PackAU(next, 93600, 93600, false), 93600, DefaultPayloadType)

	want := annexB(next)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f := <-ch:
			if bytes.Equal(f.data, want) && f.pts == 93600 {
				return // 丢包后恢复正常
			}
			// 残帧（截断的帧 1）解出与否均容忍
		case <-deadline:
			t.Fatal("complete frame after loss not recovered")
		}
	}
}

func TestUDPReceiverDuplicateLastPacket(t *testing.T) {
	recv, ch := startReceiver(t)
	s := newRTPSender(t, recv.LocalPort(), 100)
	m := NewPSMuxer()

	n := [][]byte{{0x41, 0x01}}
	ps := m.PackAU(n, 90000, 90000, false)
	s.send(t, ps, 90000, DefaultPayloadType)
	s.seq = 100 // 同序号整帧重发（含 marker 重复末包），模拟传输层重复
	s.send(t, ps, 90000, DefaultPayloadType)

	expectFrame(t, ch, annexB(n), 90000)
	select {
	case f := <-ch:
		t.Fatalf("duplicate frame leaked: % X", f.data)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestUDPReceiverStaticPTIgnored(t *testing.T) {
	recv, ch := startReceiver(t)
	s := newRTPSender(t, recv.LocalPort(), 1)
	m := NewPSMuxer()

	// PT=0（静态类型）：应被丢弃，不触发任何帧回调
	s.send(t, m.PackAU([][]byte{{0x41, 0x09}}, 90000, 90000, false), 90000, 0)
	select {
	case f := <-ch:
		t.Fatalf("static PT should be ignored: % X", f.data)
	case <-time.After(300 * time.Millisecond):
	}
	// 动态 PT（如 100）透传接收
	n := [][]byte{{0x41, 0x0A}}
	s.send(t, m.PackAU(n, 93600, 93600, false), 93600, 100)
	expectFrame(t, ch, annexB(n), 93600)
}

func TestUDPReceiverCloseIdempotent(t *testing.T) {
	recv, err := NewUDPReceiver(0)
	if err != nil {
		t.Fatalf("new receiver: %v", err)
	}
	if recv.LocalPort() <= 0 {
		t.Fatalf("local port = %d", recv.LocalPort())
	}
	recv.Start(func(frame []byte, pts uint64) {})
	if err := recv.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if err := recv.Close(); err != nil {
		t.Errorf("close again: %v", err)
	}
}
