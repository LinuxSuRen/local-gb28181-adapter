package rtspserver

import (
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// startTestServer 在随机端口上以仅 TCP 模式启动被测服务器。
func startTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	port := freeTCPPort(t)
	srv := New("127.0.0.1:"+strconv.Itoa(port), "", "", quietLogger())
	srv.PublisherWait = 3 * time.Second
	if err := srv.Start(); err != nil {
		t.Fatalf("start rtsp server: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv, "rtsp://127.0.0.1:" + strconv.Itoa(port)
}

func h264Desc() (*description.Session, *description.Media, *format.H264) {
	forma := &format.H264{
		PayloadTyp:        96,
		PacketizationMode: 1,
	}
	medi := &description.Media{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{forma},
	}
	return &description.Session{Medias: []*description.Media{medi}}, medi, forma
}

// publish 打开一个发布者客户端（不发包；RTP 包需在读者 PLAY 之后再写）。
func publish(t *testing.T, baseURL, path string) (*gortsplib.Client, *description.Media) {
	t.Helper()
	desc, medi, _ := h264Desc()
	c := &gortsplib.Client{}
	if err := c.StartRecording(baseURL+"/"+path, desc); err != nil {
		t.Fatalf("start recording: %v", err)
	}
	return c, medi
}

// writePackets 由发布者写出若干 RTP 包。
func writePackets(t *testing.T, c *gortsplib.Client, medi *description.Media, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(1000 + i),
				Timestamp:      uint32(90000 * i),
			},
			Payload: []byte{byte(i), 0xAB, 0xCD},
		}
		if err := c.WritePacketRTP(medi, pkt); err != nil {
			t.Fatalf("write rtp: %v", err)
		}
	}
}

// subscribe 打开一个读者客户端，返回收到的 RTP 包与等待函数。
func subscribe(t *testing.T, baseURL, path string) (chan *rtp.Packet, func() error) {
	t.Helper()
	u, err := base.ParseURL(baseURL + "/" + path)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	c := &gortsplib.Client{Scheme: u.Scheme, Host: u.Host}
	if err := c.Start2(); err != nil {
		t.Fatalf("client start: %v", err)
	}
	desc, _, err := c.Describe(u)
	if err != nil {
		c.Close()
		return nil, func() error { return err }
	}
	if len(desc.Medias) != 1 {
		t.Fatalf("medias = %d, want 1", len(desc.Medias))
	}
	medi := desc.Medias[0]
	if err := c.SetupAll(desc.BaseURL, desc.Medias); err != nil {
		t.Fatalf("setup: %v", err)
	}
	ch := make(chan *rtp.Packet, 64)
	c.OnPacketRTP(medi, medi.Formats[0], func(pkt *rtp.Packet) {
		ch <- pkt
	})
	if _, err := c.Play(nil); err != nil {
		t.Fatalf("play: %v", err)
	}
	t.Cleanup(c.Close)
	return ch, nil
}

func TestPublishAndSubscribe(t *testing.T) {
	_, baseURL := startTestServer(t)

	pub, medi := publish(t, baseURL, "cam/cam1")
	defer pub.Close()

	ch, waitErr := subscribe(t, baseURL, "cam/cam1")
	if waitErr != nil {
		t.Fatalf("subscribe: %v", waitErr())
	}
	writePackets(t, pub, medi, 3)
	for i := 0; i < 3; i++ {
		select {
		case pkt := <-ch:
			if len(pkt.Payload) < 2 || pkt.Payload[1] != 0xAB {
				t.Fatalf("unexpected payload: %v", pkt.Payload)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("packet %d not received", i)
		}
	}
}

func TestDescribeWaitsForPublisher(t *testing.T) {
	srv, baseURL := startTestServer(t)
	srv.PublisherWait = 5 * time.Second

	// 读者先到：describe 应阻塞等待，而不是立刻 404。
	var wg sync.WaitGroup
	wg.Add(1)
	var subErr error
	go func() {
		defer wg.Done()
		_, waitErr := subscribe(t, baseURL, "cam/cam1")
		if waitErr != nil && waitErr() != nil {
			subErr = waitErr()
		}
	}()

	time.Sleep(300 * time.Millisecond)
	pub, _ := publish(t, baseURL, "cam/cam1")
	defer pub.Close()

	wg.Wait()
	if subErr != nil {
		t.Fatalf("late publisher should satisfy waiting reader: %v", subErr)
	}
}

func TestDescribeTimeoutWithoutPublisher(t *testing.T) {
	srv, baseURL := startTestServer(t)
	srv.PublisherWait = 300 * time.Millisecond

	start := time.Now()
	_, waitErr := subscribe(t, baseURL, "cam/none")
	if waitErr == nil {
		t.Fatal("expected describe error")
	}
	if err := waitErr(); err == nil {
		t.Fatal("describe should fail when no publisher arrives")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("describe returned too fast (%v), wait not honored", elapsed)
	}
}

func TestPublisherReplacement(t *testing.T) {
	_, baseURL := startTestServer(t)

	pub1, _ := publish(t, baseURL, "cam/cam1")
	defer pub1.Close()

	// 第二个发布者接管同一路径，第一个应被断开。
	pub2, medi2 := publish(t, baseURL, "cam/cam1")
	defer pub2.Close()

	done := make(chan error, 1)
	go func() { done <- pub1.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("old publisher should be disconnected")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old publisher still connected")
	}

	// 接管后读者应能正常读取新发布者的流。
	ch, waitErr := subscribe(t, baseURL, "cam/cam1")
	if waitErr != nil {
		t.Fatalf("subscribe: %v", waitErr())
	}
	writePackets(t, pub2, medi2, 2)
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no packets from new publisher")
	}
}

func TestPublisherGoneReleasesPath(t *testing.T) {
	srv, baseURL := startTestServer(t)

	pub, _ := publish(t, baseURL, "cam/cam1")
	pub.Close() // 主动断开

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		srv.mu.Lock()
		_, exists := srv.paths["cam/cam1"]
		srv.mu.Unlock()
		if !exists {
			return // 路径已释放
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("path should be released after publisher disconnects")
}
