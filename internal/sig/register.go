package sig

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
	"github.com/linuxsuren/local-gb28181-adapter/internal/manscdp"
)

// registerLoop 注册主循环：
//   - 未注册成功前按 5s 退避持续重试；
//   - 注册成功后按 expires/2 周期性刷新注册；
//   - 心跳连续 3 次无响应视为掉线，重新走注册流程。
func (s *Service) registerLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		root := s.store.Root()
		p := root.Platform
		expires := p.RegisterExpires
		if expires <= 0 {
			expires = 3600
		}

		if err := s.registerOnce(ctx); err != nil {
			s.state.Store(StateUnregistered)
			s.regErr.Store(err.Error())
			s.logger.Warn("gb28181 register failed, retrying", "err", err.Error())
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}
		s.state.Store(StateRegistered)
		s.lastRegAt.Store(time.Now())
		s.regErr.Store("")
		s.keepFails.Store(0)
		s.logger.Info("gb28181 registered", "device", p.DeviceID, "server", p.ServerAddr)

		// 刷新周期 = expires/2；心跳循环独立运行。
		refresh := time.Duration(expires/2) * time.Second
		if refresh < 10*time.Second {
			refresh = 10 * time.Second
		}
		if !s.keepaliveUntil(ctx, refresh) {
			return
		}
		// 心跳失败导致的掉线：回到循环头重新注册。
	}
}

// keepaliveUntil 在 refresh 时限内维持心跳；返回 false 表示 ctx 取消。
// 心跳连续 3 次失败即返回 true（触发上层重注册）。
func (s *Service) keepaliveUntil(ctx context.Context, refresh time.Duration) bool {
	deadline := time.Now().Add(refresh)
	for {
		root := s.store.Root()
		interval := time.Duration(root.Platform.KeepaliveInterval) * time.Second
		if interval < 5*time.Second {
			interval = 5 * time.Second
		}
		if !sleepCtx(ctx, interval) {
			return false
		}
		if time.Now().After(deadline) {
			return true // 到刷新时间，让上层重新注册
		}
		if err := s.sendKeepalive(ctx); err != nil {
			fails := s.keepFails.Add(1)
			s.logger.Warn("gb28181 keepalive failed", "fails", fails, "err", err.Error())
			if fails >= 3 {
				s.logger.Warn("gb28181 keepalive lost 3 times, re-registering")
				return true
			}
			continue
		}
		s.keepFails.Store(0)
		s.lastKeep.Store(time.Now())
	}
}

// sendKeepalive 发送一次心跳并等待 200。
func (s *Service) sendKeepalive(ctx context.Context) error {
	root := s.store.Root()
	body := manscdp.BuildKeepalive(s.nextSN(), root.Platform.DeviceID)
	return s.sendMessage(ctx, body)
}

// registerOnce 执行一次 REGISTER（含 401 digest 挑战应答）。
func (s *Service) registerOnce(ctx context.Context) error {
	s.state.Store(StateRegistering)
	root := s.store.Root()
	p := root.Platform

	host, port, err := net.SplitHostPort(p.ServerAddr)
	if err != nil {
		return fmt.Errorf("bad platform server addr: %w", err)
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("bad platform server port %q", port)
	}
	domain := p.Domain()
	expires := p.RegisterExpires
	if expires <= 0 {
		expires = 3600
	}

	recipient := sip.Uri{Scheme: "sip", Host: host, Port: portNum, User: domain}
	req := sip.NewRequest(sip.REGISTER, recipient)
	req.SetTransport("UDP")
	req.AppendHeader(sip.NewHeader("From", fmt.Sprintf("<sip:%s@%s>", p.DeviceID, domain)))
	req.AppendHeader(sip.NewHeader("To", fmt.Sprintf("<sip:%s@%s>", p.DeviceID, domain)))
	req.AppendHeader(sip.NewHeader("Contact",
		fmt.Sprintf("<sip:%s@%s>", p.DeviceID, s.contactHost())))
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))

	res, err := s.doRequest(ctx, req, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		return fmt.Errorf("send register: %w", err)
	}
	if res.StatusCode == 401 || res.StatusCode == 407 {
		wwwAuth := res.GetHeader("WWW-Authenticate")
		if wwwAuth == nil {
			return fmt.Errorf("challenge without WWW-Authenticate header")
		}
		chal, err := digest.ParseChallenge(wwwAuth.Value())
		if err != nil {
			return fmt.Errorf("parse challenge: %w", err)
		}
		cred, err := digest.Digest(chal, digest.Options{
			Method:   "REGISTER",
			URI:      fmt.Sprintf("sip:%s", domain),
			Username: p.DeviceID,
			Password: p.Password,
		})
		if err != nil {
			return fmt.Errorf("digest: %w", err)
		}
		newReq := req.Clone()
		newReq.RemoveHeader("Via") // 由传输层重新生成
		newReq.AppendHeader(sip.NewHeader("Authorization", cred.String()))
		res, err = s.doRequest(ctx, newReq, sipgo.ClientRequestIncreaseCSEQ, sipgo.ClientRequestAddVia)
		if err != nil {
			return fmt.Errorf("send authorized register: %w", err)
		}
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("register rejected: %d %s", res.StatusCode, res.Reason)
	}
	return nil
}

// sendMessage 发送 MESSAGE（应用层 MANSCDP 消息）并等待最终应答。
func (s *Service) sendMessage(ctx context.Context, body string) error {
	root := s.store.Root()
	p := root.Platform
	host, portStr, err := net.SplitHostPort(p.ServerAddr)
	if err != nil {
		return fmt.Errorf("bad platform server addr: %w", err)
	}
	portNum, _ := strconv.Atoi(portStr)
	recipient := sip.Uri{Scheme: "sip", User: p.ServerID, Host: host, Port: portNum}
	req := sip.NewRequest(sip.MESSAGE, recipient)
	req.SetTransport("UDP")
	req.AppendHeader(sip.NewHeader("From", fmt.Sprintf("<sip:%s@%s>", p.DeviceID, p.Domain())))
	req.AppendHeader(sip.NewHeader("To", fmt.Sprintf("<sip:%s@%s>", p.ServerID, p.ServerDomain())))
	req.SetBody([]byte(body))
	req.AppendHeader(sip.NewHeader("Content-Type", "Application/MANSRTSP"))
	res, err := s.doRequest(ctx, req)
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("message rejected: %d %s", res.StatusCode, res.Reason)
	}
	return nil
}

// queryCtx 返回信令上下文（未运行时返回 nil）。
func (s *Service) queryCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.sipCtx == nil {
		return nil
	}
	return s.sipCtx
}

// doRequest 创建客户端事务并等待最终应答。
func (s *Service) doRequest(ctx context.Context, req *sip.Request, opts ...sipgo.ClientRequestOption) (*sip.Response, error) {
	client := s.client
	if client == nil {
		return nil, fmt.Errorf("sip client not running")
	}
	tx, err := client.TransactionRequest(ctx, req, opts...)
	if err != nil {
		return nil, err
	}
	defer tx.Terminate()
	select {
	case res := <-tx.Responses():
		if res.IsProvisional() {
			// 继续等最终应答（同一事务内）
			for {
				select {
				case r := <-tx.Responses():
					if r.IsProvisional() {
						continue
					}
					return r, nil
				case <-tx.Done():
					return nil, tx.Err()
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		return res, nil
	case <-tx.Done():
		return nil, tx.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// contactHost 生成 Contact 中的主机部分。
func (s *Service) contactHost() string {
	if s.AdvertiseIP != "" {
		return net.JoinHostPort(s.AdvertiseIP, strconv.Itoa(s.sipPort))
	}
	return fmt.Sprintf("127.0.0.1:%d", s.sipPort)
}

// sleepCtx 可被 ctx 取消的 sleep；返回 false 表示已取消。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
