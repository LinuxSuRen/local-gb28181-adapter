package psrtp

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
)

// MaxPacketSize 单个 RTP 包（含头）上限，避免 IP 分片。
const MaxPacketSize = 1400

// RTPHeaderSize 固定 RTP 头长度。
const RTPHeaderSize = 12

// DefaultPayloadType GB28181 PS 流常用动态载荷类型。
const DefaultPayloadType = 96

// Sender 将 PS 字节流切片为 RTP 包发往平台媒体端口。
// 支持 UDP 直发与 TCP 被动（RFC 4571：2 字节大端长度 + RTP 包）。
type Sender struct {
	mu   sync.Mutex
	conn net.Conn
	tcp  bool
	pt   uint8
	ssrc uint32
	seq  uint16

	pkt []byte // 复用的完整包缓冲（RTP 头 + 载荷）
}

// NewUDPSender 创建 UDP 发送器，dst 形如 "192.168.1.10:30000"。
func NewUDPSender(dst string, ssrc uint32) (*Sender, error) {
	conn, err := net.Dial("udp", dst)
	if err != nil {
		return nil, fmt.Errorf("dial udp %s: %w", dst, err)
	}
	return &Sender{conn: conn, pt: DefaultPayloadType, ssrc: ssrc, pkt: make([]byte, 0, MaxPacketSize+64)}, nil
}

// NewTCPSender 创建 TCP 发送器（主动连接平台监听端口，RFC 4571 帧格式）。
func NewTCPSender(dst string, ssrc uint32) (*Sender, error) {
	conn, err := net.Dial("tcp", dst)
	if err != nil {
		return nil, fmt.Errorf("dial tcp %s: %w", dst, err)
	}
	return &Sender{conn: conn, tcp: true, pt: DefaultPayloadType, ssrc: ssrc, pkt: make([]byte, 0, MaxPacketSize+64)}, nil
}

// LocalPort 返回本端媒体端口（应答 SDP m= 行使用）。
func (s *Sender) LocalPort() int {
	if s.conn == nil {
		return 0
	}
	if addr, ok := s.conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.Port
	}
	if addr, ok := s.conn.LocalAddr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

// RemoteAddr 返回对端地址描述。
func (s *Sender) RemoteAddr() string {
	if s.conn == nil {
		return ""
	}
	return s.conn.RemoteAddr().String()
}

// SendPS 发送一段 PS 字节流：按 MaxPacketSize 切片为同一时间戳的多个 RTP 包，
// 最后一包置 marker 位。
func (s *Sender) SendPS(ps []byte, ts90k uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return fmt.Errorf("sender closed")
	}
	if len(ps) == 0 {
		return nil
	}
	maxPayload := MaxPacketSize - RTPHeaderSize
	for off := 0; off < len(ps); off += maxPayload {
		end := off + maxPayload
		if end > len(ps) {
			end = len(ps)
		}
		last := end == len(ps)
		if err := s.writePacket(ps[off:end], ts90k, last); err != nil {
			return err
		}
	}
	return nil
}

// writePacket 发送单个 RTP 包。调用方持锁。
func (s *Sender) writePacket(payload []byte, ts uint32, marker bool) error {
	s.pkt = s.pkt[:0]
	s.pkt = append(s.pkt,
		0x80,                            // V=2, P=0, X=0, CC=0
		s.pt|byte(boolToInt(marker))<<7, // M 位
	)
	s.pkt = binary.BigEndian.AppendUint16(s.pkt, s.seq)
	s.pkt = binary.BigEndian.AppendUint32(s.pkt, ts)
	s.pkt = binary.BigEndian.AppendUint32(s.pkt, s.ssrc)
	s.pkt = append(s.pkt, payload...)
	s.seq++

	if s.tcp {
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(s.pkt)))
		if _, err := s.conn.Write(lenBuf[:]); err != nil {
			return fmt.Errorf("tcp write length: %w", err)
		}
	}
	if _, err := s.conn.Write(s.pkt); err != nil {
		return fmt.Errorf("write rtp: %w", err)
	}
	return nil
}

// Close 关闭底层连接（幂等）。
func (s *Sender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	conn := s.conn
	s.conn = nil
	return conn.Close()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
