// Package psrtp 实现 GB/T 28181 媒体发送：H264 帧 → MPEG-PS 封装 → RTP 打包，
// 支持 UDP 与 TCP 被动（RFC 4571 帧格式）两种传输。
package psrtp

import (
	"encoding/binary"
)

// MPEG-PS 与 H264 常量。
const (
	pesVideoStart = 0x000001E0
	streamTypeH264 = 0x1B
	streamIDVideo  = 0xE0
	annexBStart   = "\x00\x00\x00\x01"
	// muxRateUnits program_mux_rate（400bps 单位，取 10Mbps 量级），
	// 解析端通常不校验，仅需非 0 且带 marker 位。
	muxRateUnits = 25000
)

// PSMuxer 将 H264 访问单元（access unit）封装为 PS 节目流。
// 非并发安全，由所属会话单线程调用。
type PSMuxer struct {
	scr    int64  // 上一次 pack 的 SCR（90kHz），保证单调
	psm    []byte
	buf    []byte // PackAU 输出缓冲（复用）
	esBuf  []byte // Annex-B 码流拼装缓冲（复用）
}

// NewPSMuxer 创建封装器。
func NewPSMuxer() *PSMuxer {
	return &PSMuxer{psm: buildPSM()}
}

// PackAU 封装一个访问单元，返回完整 PS 段（PSM? + pack header + PES）。
// nalus 为裸 NAL（不含起始码）；pts/dts 为 90kHz 时钟；
// idr 为关键帧时在 pack 前插入 PSM（码流描述），保证随机接入可解。
// 返回切片复用内部缓冲：下一次 PackAU 前发送完毕即可。
func (m *PSMuxer) PackAU(nalus [][]byte, pts, dts int64, idr bool) []byte {
	m.buf = m.buf[:0]
	if idr {
		m.buf = append(m.buf, m.psm...)
	}
	scr := dts
	if scr <= m.scr {
		scr = m.scr + 1
	}
	m.scr = scr
	m.buf = appendPackHeader(m.buf, scr)
	m.buf = appendPES(m.buf, nalus, pts, dts)
	return m.buf
}

// ---- pack header ----

// appendPackHeader 写入 14 字节 pack header：
// 起始码 + SCR(6B) + program_mux_rate(3B) + reserved/stuffing(1B)。
func appendPackHeader(dst []byte, scrBase int64) []byte {
	dst = append(dst, 0x00, 0x00, 0x01, 0xBA)
	dst = appendSCR(dst, scrBase)
	rate := uint32(muxRateUnits)
	// 22 bits + '11'
	dst = append(dst,
		byte(rate>>14),
		byte(rate>>6)&0xFF,
		(byte(rate)<<2)|0x03,
	)
	// reserved(5b=11111) + stuffing_length(3b=000)
	dst = append(dst, 0xF8)
	return dst
}

// appendSCR 写入 6 字节 SCR（base 33b + ext 9b，含全部 marker 位）。
func appendSCR(dst []byte, base int64) []byte {
	const ext = 0
	bits := uint64(0)
	bits |= 1 << 46 // '01'
	bits |= uint64(base&0x7) << 43
	bits |= 1 << 42
	bits |= uint64((base>>15)&0x7FFF) << 27
	bits |= 1 << 26
	bits |= uint64(base&0x7FFF) << 11
	bits |= 1 << 10
	bits |= uint64(ext&0x1FF) << 1
	bits |= 1
	return append(dst,
		byte(bits>>40),
		byte(bits>>32),
		byte(bits>>24),
		byte(bits>>16),
		byte(bits>>8),
		byte(bits),
	)
}

// ---- PES ----

// appendPES 写入一个视频 PES 包：起始码 + 不定长 + PTS（可选 DTS）+ Annex-B ES。
func appendPES(dst []byte, nalus [][]byte, pts, dts int64) []byte {
	dst = append(dst, 0x00, 0x00, 0x01, streamIDVideo)
	// PES_packet_length = 0：PS 中视频流允许不定长。
	dst = append(dst, 0x00, 0x00)
	// '10' + 标志位全 0（无加密、数据对齐）。
	dst = append(dst, 0x80)
	hasDTS := dts != pts
	if hasDTS {
		dst = append(dst, 0xC0, 10)
		dst = appendTimestamp(dst, 0x2, pts) // '0010' 前缀 = PTS
		dst = appendTimestamp(dst, 0x1, dts) // '0001' 前缀 = DTS
	} else {
		dst = append(dst, 0x80, 5)
		dst = appendTimestamp(dst, 0x2, pts)
	}
	for _, nalu := range nalus {
		dst = append(dst, annexBStart...)
		dst = append(dst, nalu...)
	}
	return dst
}

// appendTimestamp 写入 5 字节时间戳，prefix 为 4 bit 标识（PTS=0b0010，DTS=0b0001）。
func appendTimestamp(dst []byte, prefix byte, ts int64) []byte {
	t := uint64(ts) & 0x1FFFFFFFF // 33 bit
	return append(dst,
		(prefix<<4)|byte((t>>30)&0x7)<<1|1,
		byte((t>>22)&0xFF),
		byte((t>>15)&0x7F)<<1|1,
		byte((t>>7)&0xFF),
		byte(t&0x7F)<<1|1,
	)
}

// ---- PSM（程序流映射）----

// buildPSM 构造 H264 视频流的固定 PSM（含 CRC32-MPEG 校验）。
func buildPSM() []byte {
	body := []byte{
		0xE1,       // current_next=1, version=1
		0xFF,       // marker + reserved
		0x00, 0x00, // program_stream_info_length = 0
		0x00, 0x04, // elementary_stream_map_length = 4
		streamTypeH264,
		streamIDVideo,
		0xF0, 0x00, // marker + elementary_stream_info_length = 0
	}
	out := make([]byte, 0, len(body)+10)
	out = append(out, 0x00, 0x00, 0x01, 0xBC)
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(body)+4)) // 含 CRC
	out = append(out, lenBuf[:]...)
	out = append(out, body...)
	var crcBuf [4]byte
	binary.BigEndian.PutUint32(crcBuf[:], crc32MPEG(body))
	out = append(out, crcBuf[:]...)
	return out
}

// crc32MPEG 计算 CRC-32/MPEG-2（poly 0x04C11DB7，init 0xFFFFFFFF，
// 无反转、无最终异或）。
func crc32MPEG(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
