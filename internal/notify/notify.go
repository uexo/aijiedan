// Package notify 提供平台通知层。
// M0: 站内信(默认)落库即可达; 邮件/短信为骨架 Provider, 经 env 切换, 不调外部。
// 设计对齐 P2-2 的 Provider 模式: 业务落库(持久化) + 外部通道异步外发(可切换)。
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strconv"
	"sync"
	"time"

	"lianvo/internal/model"
	"lianvo/internal/store"
)

// Provider 外部通知通道(邮件/短信)。站内信(inapp)由 Service 直接落库, 不经 Provider。
type Provider interface {
	Name() string
	// Send 向外部通道投递通知(邮件/短信网关)。stub 时仅模拟返回外部ID。
	Send(ctx context.Context, uid, title, body string) (extID string, err error)
}

// Service 通知业务: 落库 → 若 channel 对应 Provider 已配置则异步外发。
type Service struct {
	st    store.Store
	email Provider // 邮件通道(nil 表示不启用)
	sms   Provider // 短信通道(nil 表示不启用)
	mu    sync.RWMutex
	subs  map[string][]chan *model.Notification
}

// NewService 构造通知服务。email/sms 为 nil 时仅站内信(落库)。
func NewService(st store.Store, email, sms Provider) *Service {
	return &Service{st: st, email: email, sms: sms, subs: map[string][]chan *model.Notification{}}
}

// NewStubEmailProvider / NewStubSMSProvider 构造骨架 Provider(默认不启用, 不调外部)。
func NewStubEmailProvider() Provider { return &StubEmailProvider{} }
func NewStubSMSProvider() Provider  { return &StubSMSProvider{} }

// ---- Providers (骨架) ----

// StubEmailProvider 默认邮件骨架: 不调外部, 仅模拟返回外部ID。
type StubEmailProvider struct{}

func (StubEmailProvider) Name() string { return "stub-email" }
func (StubEmailProvider) Send(_ context.Context, uid, title, body string) (string, error) {
	return "mail-" + uid + "-" + fmt.Sprintf("%d", model.Now()), nil
}

// StubSMSProvider 默认短信骨架: 不调外部, 仅模拟返回外部ID。
type StubSMSProvider struct{}

func (StubSMSProvider) Name() string { return "stub-sms" }
func (StubSMSProvider) Send(_ context.Context, uid, title, body string) (string, error) {
	return "sms-" + uid + "-" + fmt.Sprintf("%d", model.Now()), nil
}

// ---- 真实形态 Provider(填凭证即启用, 不调外部时由 main 决定是否注入) ----

// SMTPEmailProvider 真实 SMTP 邮件发送(生产形态)。
// 字段由 env(LIANVO_SMTP_*)注入; uid 须为收件人邮箱(生产应解析 uid→email)。
type SMTPEmailProvider struct {
	Host, Port, User, Pass, From string
}

func (p SMTPEmailProvider) Name() string { return "smtp" }
func (p SMTPEmailProvider) Send(_ context.Context, uid, title, body string) (string, error) {
	if p.Host == "" || p.User == "" {
		return "", fmt.Errorf("SMTP 未配置")
	}
	addr := p.Host + ":" + p.Port
	msg := []byte("From: " + p.From + "\r\n" +
		"To: " + uid + "\r\n" +
		"Subject: " + title + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		body)
	if err := smtp.SendMail(addr, smtp.PlainAuth("", p.User, p.Pass, p.Host), p.From, []string{uid}, msg); err != nil {
		return "", err
	}
	return "mail-" + uid, nil
}

// HTTPSMSProvider 真实形态短信网关(通用 HTTP + HMAC 签名骨架, 按运营商调整字段)。
// GatewayURL/SecretId/SecretKey/模板由 env 注入; uid 须为手机号(生产应解析 uid→phone)。
type HTTPSMSProvider struct {
	GatewayURL, SecretId, SecretKey, Tpl string
}

func (p HTTPSMSProvider) Name() string { return "http-sms" }
func (p HTTPSMSProvider) Send(_ context.Context, uid, title, body string) (string, error) {
	if p.GatewayURL == "" {
		return "", fmt.Errorf("短信网关未配置")
	}
	payload := map[string]interface{}{"phone": uid, "title": title, "content": body, "template": p.Tpl}
	b, _ := json.Marshal(payload)
	// 示例签名(HMAC-SHA256), 实际按短信服务商要求调整 header/签名规则。
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(p.SecretKey))
	mac.Write([]byte(ts + string(b)))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	req, err := http.NewRequest("POST", p.GatewayURL, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Id", p.SecretId)
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Sign", sign)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 256)
	for {
		n, e := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if e != nil {
			break
		}
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("短信网关 HTTP %d: %s", resp.StatusCode, string(buf))
	}
	return "sms-" + uid, nil
}

// Notify 发送一条通知: 落库(站内信默认) → 若 channel 对应 Provider 已配置则异步外发。
func (s *Service) Notify(uid, channel, title, body string) *model.Notification {
	if uid == "" {
		return nil
	}
	if channel == "" {
		channel = model.ChannelInApp
	}
	n := &model.Notification{
		ID:        s.st.NextID("N"),
		UID:       uid,
		Channel:   channel,
		Title:     title,
		Body:      body,
		Status:    model.NotiUnread,
		CreatedAt: model.Now(),
	}
	s.st.SaveNotification(n)
	s.push(n)
	// 外部通道(异步, 失败不影响落库)
	if channel == model.ChannelEmail && s.email != nil {
		go func() { _, _ = s.email.Send(context.Background(), uid, title, body) }()
	}
	if channel == model.ChannelSMS && s.sms != nil {
		go func() { _, _ = s.sms.Send(context.Background(), uid, title, body) }()
	}
	return n
}

// Subscribe 订阅某用户的通知推送(供 SSE/轮询扩展; 当前控制台/M5 走拉取)。
func (s *Service) Subscribe(uid string) (chan *model.Notification, func()) {
	s.mu.Lock()
	ch := make(chan *model.Notification, 16)
	s.subs[uid] = append(s.subs[uid], ch)
	s.mu.Unlock()
	unsub := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		subs := s.subs[uid]
		for i, c := range subs {
			if c == ch {
				s.subs[uid] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		close(ch)
	}
	return ch, unsub
}

func (s *Service) push(n *model.Notification) {
	s.mu.RLock()
	subs := append([]chan *model.Notification{}, s.subs[n.UID]...)
	s.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- n:
		default:
		}
	}
}

// List 列出某用户的通知(按创建时间倒序, limit<=0 表示默认 50)。
func (s *Service) List(uid string, limit int) []*model.Notification {
	if limit <= 0 {
		limit = 50
	}
	return s.st.ListNotifications(uid, limit)
}

// UnreadCount 某用户未读通知数。
func (s *Service) UnreadCount(uid string) int {
	return s.st.CountUnread(uid)
}

// MarkRead 标记某条通知已读(须属于该用户), 返回更新后的通知; 不存在/越权返回 (nil,false)。
func (s *Service) MarkRead(uid, id string) (*model.Notification, bool) {
	return s.st.MarkNotificationRead(uid, id)
}
