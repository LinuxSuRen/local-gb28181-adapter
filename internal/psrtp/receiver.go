package psrtp

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"

	"github.com/pion/rtp"
)

// PS 层起始码（00 00 01 + 1 字节系统码或流 ID）相关常量。
const (
	psProgramEndID = 0xB9 // program_end_code，无长度域
	psPackCode     = 0xBA // pack header
	psSysHeaderID  = 0xBB // system header
	psPSMID        = 0xBC // program stream map
	psPaddingID    = 0xBE // padding stream（裸载荷）
	psPrivate2ID   = 0xBF // private stream 2（裸载荷）
	psVideoIDFirst = 0xE0 // 视频流 ID 下界（E0~EF，GB28181 H264 通常为 E0）
	psVideoIDLast  = 0xEF
	// psIDMin 为 PS 层起始码第 4 字节下界：>= 0xB9 的系统码或 PES 流 ID
	// 最高位均为 1；AnnexB H264 的 NAL 头 forbidden_zero 位恒为 0
	// （首字节 <= 0x7F），因此扫描视频 ES 时不会误判 PS 层边界。
	psIDMin = 0xB9
)

// packHeaderLen MPEG-2 pack header 固定长度：起始码 4B + SCR 6B +
// program_mux_rate 3B + 1B（高 5 位保留，低 3 位为 stuffing 字节数）。
const packHeaderLen = 14

// DemuxPS 将一个 PS 帧（通常对应一个 H264 访问单元）解出视频 ES 与 PTS。
// video 为帧内全部视频 PES（流 ID E0~EF）的 ES 依序拼接——即带起始码的
// AnnexB H264 码流，无需再做格式转换；pts 取第一个携带 PTS 的视频 PES
// （90kHz 单位）。无视频 PES 或残缺不可解时返回 hasVideo=false 且 video 为
// nil；语法错位不报错，靠起始码扫描重新对齐（PS 自同步），解不出视频即整帧丢弃。
func DemuxPS(ps []byte) (video []byte, pts uint64, hasVideo bool) {
	i := 0
	for i+4 <= len(ps) {
		if ps[i] != 0 || ps[i+1] != 0 || ps[i+2] != 1 || ps[i+3] < psIDMin {
			i++ // 丢包残骸或语法错位：逐字节前进重新对齐
			continue
		}
		id := ps[i+3]
		switch {
		case id == psPackCode:
			// pack header：14B 定长 + 低 3 位指示的 stuffing 字节
			if i+packHeaderLen > len(ps) {
				return
			}
			i += packHeaderLen + int(ps[i+13]&0x07)
		case id == psSysHeaderID || id == psPSMID || id == psPaddingID || id == psPrivate2ID:
			// system header / PSM / padding / private2：2 字节长度域，整段跳过
			if i+6 > len(ps) {
				return
			}
			i += 6 + int(binary.BigEndian.Uint16(ps[i+4:i+6]))
		case id == psProgramEndID:
			i += 4
		case id >= psVideoIDFirst && id <= psVideoIDLast:
			// 视频 PES：取出 ES（AnnexB H264）
			esStart, end, p, hasPTS, ok := parsePES(ps, i)
			if !ok {
				return
			}
			if hasPTS && !hasVideo {
				pts = p // 取第一个视频 PES 的 PTS
			}
			if esStart < end {
				video = append(video, ps[esStart:end]...)
				hasVideo = true
			}
			i = end
		default:
			// 其余 PES（音频 C0~DF、私有 BD、保留流）：解出头后整段跳过
			_, end, _, _, ok := parsePES(ps, i)
			if !ok {
				return
			}
			i = end
		}
	}
	return video, pts, hasVideo
}

// parsePES 解析 ps[i:] 处的 PES 头，返回 ES 起始偏移 esStart、包尾偏移 end
// 与 PTS。头部布局：起始码 3B + stream_id 1B + PES_packet_length 2B（视频流
// 允许为 0 表示不定长）+ flags 2B（第 1 字节高 2 位 '10'，第 2 字节高 2 位为
// PTS_DTS_flags）+ PES_header_data_length 1B + header data。PTS 存在时
// （PTS_DTS_flags 非 0）位于 header data 前 5 字节。PES_packet_length 为 0 时
// 通过扫描下一个 PS 层起始码确定包尾。残缺时返回 ok=false。
func parsePES(ps []byte, i int) (esStart, end int, pts uint64, hasPTS, ok bool) {
	if i+9 > len(ps) {
		return 0, 0, 0, false, false
	}
	pesLen := int(binary.BigEndian.Uint16(ps[i+4 : i+6]))
	hdrDataLen := int(ps[i+8])
	esStart = i + 9 + hdrDataLen
	if esStart > len(ps) {
		return 0, 0, 0, false, false
	}
	if ps[i+7]&0x80 != 0 && i+14 <= len(ps) {
		pts = decodePTS(ps[i+9 : i+14])
		hasPTS = true
	}
	if pesLen > 0 {
		end = i + 6 + pesLen
		if end > len(ps) {
			end = len(ps)
		}
	} else {
		end = scanPSStart(ps, esStart, len(ps))
	}
	return esStart, end, pts, hasPTS, true
}

// decodePTS 从 5 字节 PES 时间戳还原 33bit 值（90kHz 单位）：
// PTS = b1>>1<<30 | b2<<22 | b3>>1<<15 | b4<<7 | b5>>1。
func decodePTS(b []byte) uint64 {
	return uint64(b[0]>>1&0x07)<<30 | uint64(b[1])<<22 |
		uint64(b[2]>>1&0x7F)<<15 | uint64(b[3])<<7 | uint64(b[4]>>1&0x7F)
}

// scanPSStart 在 ps[from:end] 内查找下一个 PS 层起始码（00 00 01 且第 4 字节
// >= psIDMin），返回其偏移；找不到返回 end。
func scanPSStart(ps []byte, from, end int) int {
	for i := from; i+4 <= end; i++ {
		if ps[i] == 0 && ps[i+1] == 0 && ps[i+2] == 1 && ps[i+3] >= psIDMin {
			return i
		}
	}
	return end
}

// UDPReceiver GB28181 平台侧 UDP 收流器：接收下级设备（如海康相机）经 INVITE
// 指定端口推来的 RTP(PS) 流，按 RTP 时间戳/序号重组 PS 帧，解出 H264 AnnexB
// 帧后回调。重组状态仅由收包 goroutine 访问；Close 可从任意 goroutine 调用。
type UDPReceiver struct {
	conn      *net.UDPConn
	closeOnce sync.Once

	// 以下重组状态仅收包 goroutine 访问，无需加锁。
	buf     []byte // 当前时间戳组的载荷拼接缓冲（复用）
	curTS   uint32 // 当前组 RTP 时间戳
	inGroup bool   // 是否存在进行中的组
	maxSeq  uint16 // 组内已见最大序号
	hasSeq  bool
}

// NewUDPReceiver 创建 UDP 收流器并绑定 0.0.0.0:port；port 为 0 时由系统随机分配。
func NewUDPReceiver(port int) (*UDPReceiver, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return nil, fmt.Errorf("listen udp :%d: %w", port, err)
	}
	return &UDPReceiver{conn: conn}, nil
}

// LocalPort 返回绑定的本地 UDP 端口（应答 INVITE 时 SDP m= 行收流端口）。
func (r *UDPReceiver) LocalPort() int {
	return r.conn.LocalAddr().(*net.UDPAddr).Port
}

// Start 启动收包 goroutine。onFrame 在收包 goroutine 中回调：frame 为解出的
// H264 AnnexB 帧（每次回调新分配，回调后可安全持有），pts 为 90kHz 时间戳。
// 不接收 context，通过 Close 停止；重复调用 Start 不受支持。
func (r *UDPReceiver) Start(onFrame func(frame []byte, pts uint64)) {
	go r.loop(onFrame)
}

// Close 关闭收流器（幂等）。收包 goroutine 随之退出，onFrame 不再回调。
func (r *UDPReceiver) Close() error {
	r.closeOnce.Do(func() {
		_ = r.conn.Close()
	})
	return nil
}

// loop 收包主循环：ReadFromUDP → 解析 RTP → 重组 → flush。连接关闭后退出。
func (r *UDPReceiver) loop(onFrame func(frame []byte, pts uint64)) {
	buf := make([]byte, 2048)
	for {
		n, _, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return // Close 触发读错误退出
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue // 非 RTP 包（版本错乱等），丢弃
		}
		r.handleRTP(pkt, onFrame)
	}
}

// handleRTP 处理单个 RTP 包（仅收包 goroutine 调用）。
// 载荷类型按 GB28181 惯例宽松处理：静态类型（PT < 96）丢弃，动态类型透传；
// 按 SSRC 不做过滤（单端口单流场景）。
func (r *UDPReceiver) handleRTP(p *rtp.Packet, onFrame func(frame []byte, pts uint64)) {
	if p.PayloadType < DefaultPayloadType {
		return
	}
	if !r.inGroup || p.Timestamp != r.curTS {
		if r.inGroup {
			// 时间戳切换：上一组即使未收到 marker（尾包丢失）也照样 flush，
			// PS 自同步可容忍丢包，解不出视频的残帧由 DemuxPS 自然丢弃。
			r.flush(onFrame)
		}
		r.curTS = p.Timestamp
		r.inGroup = true
		r.buf = r.buf[:0]
		r.hasSeq = false
	}
	if len(p.Payload) == 0 {
		return
	}
	if r.hasSeq {
		// int16 差值天然处理 65535→0 序号回绕。
		if d := int16(p.SequenceNumber - r.maxSeq); d <= 0 {
			// 乱序/重复（序号不高于组内已见最大值；回退 3 以内为常见迟到或
			// 重发包，更大回退多为已 flush 组的迟到包）：丢弃后继续。
			return
		}
		// d > 0 正常推进；d > 1 表示中间丢包，容忍并继续拼接。
		r.maxSeq = p.SequenceNumber
	} else {
		r.maxSeq = p.SequenceNumber
		r.hasSeq = true
	}
	r.buf = append(r.buf, p.Payload...)
	if p.Marker {
		// marker 优先触发：末包到达立即 flush，不必等时间戳变化。
		r.flush(onFrame)
	}
}

// flush 把当前组拼出的 PS 帧交给 DemuxPS，解出视频则回调 onFrame。
func (r *UDPReceiver) flush(onFrame func(frame []byte, pts uint64)) {
	if len(r.buf) == 0 {
		return
	}
	if video, pts, ok := DemuxPS(r.buf); ok {
		onFrame(video, pts)
	}
	r.buf = r.buf[:0]
}
