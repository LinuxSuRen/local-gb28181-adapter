package psrtp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// startCodePrefix 检查 3 字节起始码。
func startCodePrefix(b []byte, off int) bool {
	return b[off] == 0 && b[off+1] == 0 && b[off+2] == 1
}

func TestPSMLayout(t *testing.T) {
	psm := buildPSM()
	if !startCodePrefix(psm, 0) || psm[3] != 0xBC {
		t.Fatalf("bad PSM start code: % X", psm[:4])
	}
	length := int(binary.BigEndian.Uint16(psm[4:6]))
	if length != len(psm)-6 {
		t.Fatalf("PSM length %d != body %d", length, len(psm)-6)
	}
	body := psm[6 : len(psm)-4]
	if body[0] != 0xE1 || body[1] != 0xFF {
		t.Errorf("version/marker bytes: % X", body[:2])
	}
	if binary.BigEndian.Uint16(body[2:4]) != 0 {
		t.Error("info length should be 0")
	}
	if binary.BigEndian.Uint16(body[4:6]) != 4 {
		t.Error("map length should be 4")
	}
	if body[6] != streamTypeH264 || body[7] != streamIDVideo {
		t.Errorf("stream map: % X", body[6:10])
	}
	crc := binary.BigEndian.Uint32(psm[len(psm)-4:])
	if crc != crc32MPEG(body) {
		t.Error("CRC mismatch")
	}
}

// crc32MPEG 已知校验值："123456789" → 0x0376E6E7
func TestCRC32MPEG(t *testing.T) {
	if got := crc32MPEG([]byte("123456789")); got != 0x0376E6E7 {
		t.Errorf("crc32MPEG = %08X", got)
	}
}

func TestPackAUBasic(t *testing.T) {
	m := NewPSMuxer()
	idr := [][]byte{
		[]byte{0x67, 0x64}, // SPS
		[]byte{0x68, 0xEB}, // PPS
		[]byte{0x65, 0x11, 0x22},
	}
	ps := m.PackAU(idr, 90000, 90000, true)

	// 结构：PSM + pack header + PES
	if !startCodePrefix(ps, 0) || ps[3] != 0xBC {
		t.Fatalf("IDR AU should start with PSM: % X", ps[:4])
	}
	psmLen := 6 + int(binary.BigEndian.Uint16(ps[4:6]))
	packOff := psmLen
	if !startCodePrefix(ps, packOff) || ps[packOff+3] != 0xBA {
		t.Fatalf("pack header expected at %d", packOff)
	}
	pesOff := packOff + 14
	if !startCodePrefix(ps, pesOff) || ps[pesOff+3] != 0xE0 {
		t.Fatalf("PES expected at %d", pesOff)
	}
	// PES 头解析
	p := ps[pesOff:]
	if binary.BigEndian.Uint16(p[4:6]) != 0 {
		t.Error("PES packet length should be 0")
	}
	if p[6] != 0x80 {
		t.Errorf("PES flags1 = %02X", p[6])
	}
	if p[7] != 0x80 || p[8] != 5 {
		t.Errorf("PTS-only flags: %02X %02X", p[7], p[8])
	}
	// ES 应包含全部 NAL（各带起始码）
	es := p[9+5:]
	var want []byte
	for _, nalu := range idr {
		want = append(want, annexBStart...)
		want = append(want, nalu...)
	}
	if !bytes.Equal(es, want) {
		t.Errorf("ES mismatch\n got: % X\nwant: % X", es, want)
	}
}

func TestPackAUPOnly(t *testing.T) {
	m := NewPSMuxer()
	ps := m.PackAU([][]byte{{0x41, 0x00}}, 90300, 90300, false)
	if startCodePrefix(ps, 0) && ps[3] == 0xBC {
		t.Error("non-IDR AU should not carry PSM")
	}
	if !startCodePrefix(ps, 0) || ps[3] != 0xBA {
		t.Fatalf("should start with pack header: % X", ps[:4])
	}
}

func TestPackAUWithDTS(t *testing.T) {
	m := NewPSMuxer()
	ps := m.PackAU([][]byte{{0x65, 0x01}}, 180000, 179700, true)
	// 跳过 PSM 与 pack header 找 PES
	off := 0
	for {
		if startCodePrefix(ps, off) && ps[off+3] == 0xE0 {
			break
		}
		off++
		if off > len(ps)-4 {
			t.Fatal("no PES found")
		}
	}
	p := ps[off:]
	if p[7] != 0xC0 || p[8] != 10 {
		t.Errorf("PTS+DTS flags: %02X %02X", p[7], p[8])
	}
	pts := decodeTimestamp(p[9:14])
	dts := decodeTimestamp(p[14:19])
	if pts != 180000 || dts != 179700 {
		t.Errorf("pts=%d dts=%d", pts, dts)
	}
}

func TestSCRMonotonic(t *testing.T) {
	m := NewPSMuxer()
	ps1 := m.PackAU([][]byte{{0x41}}, 90000, 90000, false)
	if !startCodePrefix(ps1, 0) || ps1[3] != 0xBA {
		t.Fatalf("pack header: % X", ps1[:4])
	}
	// PackAU 返回复用缓冲，第二次调用会覆盖，先取出 SCR 再做对比。
	var hdr1 [14]byte
	copy(hdr1[:], ps1[:14])
	ps2 := m.PackAU([][]byte{{0x41}}, 90000, 90000, false) // 相同时间戳，SCR 仍须递增
	scr1 := decodeSCR(hdr1[4:10])
	scr2 := decodeSCR(ps2[4:10])
	if scr2 <= scr1 {
		t.Errorf("SCR must increase: %d -> %d", scr1, scr2)
	}
}

func TestSCRBitLayout(t *testing.T) {
	// 用已知小值手算 SCR base=5, ext=0：
	// '01' 101 '1' 15b '1' 15b '1' 9b '1'
	var dst []byte
	dst = appendSCR(dst, 5)
	// 反解
	bits := uint64(dst[0])<<40 | uint64(dst[1])<<32 | uint64(dst[2])<<24 |
		uint64(dst[3])<<16 | uint64(dst[4])<<8 | uint64(dst[5])
	base := (bits >> 43 & 0x7) | (bits >> 27 & 0x7FFF) << 15 | (bits >> 11 & 0x7FFF)
	if base != 5 {
		t.Errorf("SCR base roundtrip = %d", base)
	}
	// marker 位
	for _, pos := range []uint{42, 26, 10, 0} {
		if bits&(1<<pos) == 0 {
			t.Errorf("marker bit %d not set", pos)
		}
	}
	if bits&(1<<46) == 0 {
		t.Error("prefix bit 46 not set")
	}
}

// decodeTimestamp 从 5 字节 PES 时间戳还原 33bit 值（测试用）。
func decodeTimestamp(b []byte) int64 {
	return int64(uint64(b[0]>>1&0x7)<<30 |
		uint64(b[1])<<22 |
		uint64(b[2]>>1&0x7F)<<15 |
		uint64(b[3])<<7 |
		uint64(b[4]>>1&0x7F))
}

// decodeSCR 从任意缓冲前 6 字节粗解 SCR（测试用，可能读到无效数据）。
func decodeSCR(b []byte) int64 {
	if len(b) < 6 {
		return 0
	}
	bits := uint64(b[0])<<40 | uint64(b[1])<<32 | uint64(b[2])<<24 |
		uint64(b[3])<<16 | uint64(b[4])<<8 | uint64(b[5])
	return int64((bits >> 43 & 0x7) | (bits >> 27 & 0x7FFF) << 15 | (bits >> 11 & 0x7FFF))
}
