// Package rtspush 把 GB28181 点播收流解出的 H264 AnnexB 帧转发到内嵌 RTSP
// 服务器（gortsplib 客户端 ANNOUNCE/RECORD），使国标下级设备的画面可经
// RTSP 分发、抓帧预览。
package rtspush

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
)

// Sink 按通道把帧推到内嵌 RTSP 服务器（实现 uplevel.StreamSink）。
type Sink struct {
	// BaseURL 内嵌 RTSP 服务器基地址，如 rtsp://127.0.0.1:8554。
	BaseURL string

	logger *slog.Logger
}

// NewSink 创建推流出口。
func NewSink(baseURL string, logger *slog.Logger) *Sink {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sink{BaseURL: strings.TrimRight(baseURL, "/"), logger: logger}
}

// OpenChannel 打开通道的帧出口；路径形如 /gb/<channelID>。
func (s *Sink) OpenChannel(channelID string) (*Publisher, error) {
	url := fmt.Sprintf("%s/gb/%s", s.BaseURL, channelID)
	return NewPublisher(url, s.logger), nil
}

// Publisher 一个通道的 RTSP 推流器：收到首个含 SPS/PPS 的帧后自动开始
// ANNOUNCE/RECORD；此前到达的帧缓存（最多 maxPending 帧）。
type Publisher struct {
	url    string
	logger *slog.Logger

	mu      sync.Mutex
	closed  bool
	started bool
	client  *gortsplib.Client
	media   *description.Media
	enc     *rtph264.Encoder
	sps     []byte
	pps     []byte
	pending []pendingFrame
}

// maxPending 等待 SPS/PPS 期间缓存的帧数上限（防止长时间黑屏前堆积）。
const maxPending = 30

type pendingFrame struct {
	frame []byte
	pts   uint64
}

// NewPublisher 创建推流器。
func NewPublisher(url string, logger *slog.Logger) *Publisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Publisher{url: url, logger: logger}
}

// WriteFrame 写入一个 H264 AnnexB 访问单元（pts 90kHz）。
func (p *Publisher) WriteFrame(frame []byte, pts uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("publisher closed")
	}

	var au h264.AnnexB
	if err := au.Unmarshal(frame); err != nil {
		return fmt.Errorf("parse annexb: %w", err)
	}

	// 提取 SPS/PPS（海康 IDR 前通常带参数集）。
	for _, nalu := range au {
		switch nalu[0] & 0x1F {
		case 7:
			p.sps = append([]byte(nil), nalu...)
		case 8:
			p.pps = append([]byte(nil), nalu...)
		}
	}

	if !p.started {
		if p.sps == nil || p.pps == nil {
			// 参数集未就绪：缓存，等 IDR 帧一起发。
			if len(p.pending) < maxPending {
				p.pending = append(p.pending, pendingFrame{append([]byte(nil), frame...), pts})
			}
			return nil
		}
		if err := p.startLocked(); err != nil {
			return err
		}
	}

	p.writeLocked([][]byte(au), pts)
	return nil
}

// startLocked 建立到内嵌 RTSP 服务器的推流会话（调用方持锁）。
func (p *Publisher) startLocked() error {
	forma := &format.H264{
		PayloadTyp:        96,
		SPS:               p.sps,
		PPS:               p.pps,
		PacketizationMode: 1,
	}
	desc := &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Control: "stream",
			Formats: []format.Format{forma},
		}},
	}
	client := &gortsplib.Client{
		WriteTimeout: 5 * time.Second,
		ReadTimeout:  10 * time.Second,
	}
	if err := client.StartRecording(p.url, desc); err != nil {
		return fmt.Errorf("start recording %s: %w", p.url, err)
	}
	enc := &rtph264.Encoder{PayloadType: 96, PacketizationMode: 1}
	if err := enc.Init(); err != nil {
		client.Close()
		return fmt.Errorf("init rtp encoder: %w", err)
	}
	p.client, p.media, p.enc = client, desc.Medias[0], enc
	p.started = true

	// 补发等待参数集期间缓存的帧。
	for _, pf := range p.pending {
		var au h264.AnnexB
		if err := au.Unmarshal(pf.frame); err == nil {
			p.writeLocked(au, pf.pts)
		}
	}
	p.pending = nil
	p.logger.Info("rtspush publisher started", "url", p.url)
	return nil
}

// writeLocked 编码并发送一帧（调用方持锁）。
func (p *Publisher) writeLocked(au [][]byte, pts uint64) {
	pkts, err := p.enc.Encode(au)
	if err != nil {
		p.logger.Warn("rtspush encode failed", "err", err.Error())
		return
	}
	ts := uint32(pts) // PS PTS 与 RTP 同为 90kHz
	for _, pkt := range pkts {
		pkt.Timestamp = ts
		if err := p.client.WritePacketRTP(p.media, pkt); err != nil {
			p.logger.Warn("rtspush write failed", "err", err.Error())
			return
		}
	}
}

// Close 关闭推流会话。
func (p *Publisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.pending = nil
	if p.client != nil {
		p.client.Close()
		p.client = nil
	}
}
