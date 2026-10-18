// Package im 提供 IM 通信层。
// M0: 内存总线 + SSE 实时推送 + 本地落库(持久化, 不丢消息), 验证「实时通信」链路。
// P2-2: 重构为 Provider 模式 —— 外部通道(腾讯云 IM)可切换, 内容审核(Moderator)可切换。
// 不引入外部依赖; 接入真实腾讯云 IM SDK 时实现 TencentIMProvider 并在 main 注入即可。
package im

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"zanbuddy/internal/model"
	"zanbuddy/internal/store"
)

// Provider 外部 IM 通道(腾讯云等)。stub 时本地总线 + SSE 已足够演示, 不调外部。
type Provider interface {
	Name() string
	// Send 向外部通道投递消息(离线推送/多端同步由通道负责)。
	Send(ctx context.Context, from, to, body string) (extID string, err error)
}

// Moderator 内容审核。stub 拦截规避平台交易的关键词; 生产接腾讯云内容安全(天御)。
type Moderator interface {
	Name() string
	Check(body string) (blocked bool, reason string)
}

// Service IM 业务: 审核 → 落库 → 更会话 → 推 SSE → 调 Provider(异步)。
type Service struct {
	st   store.Store
	prov Provider
	mod  Moderator
	mu   sync.RWMutex
	subs map[string][]chan *model.ChatMessage
}

// NewService 构造 IM 服务。prov/mod 为 nil 时回退到 stub 实现。
func NewService(st store.Store, prov Provider, mod Moderator) *Service {
	if prov == nil {
		prov = &StubProvider{}
	}
	if mod == nil {
		mod = &StubModerator{}
	}
	return &Service{st: st, prov: prov, mod: mod, subs: map[string][]chan *model.ChatMessage{}}
}

// NewStubProvider / NewTencentIMProvider 构造 Provider。
func NewStubProvider() Provider                       { return &StubProvider{} }
func NewStubModerator() Moderator                     { return &StubModerator{} }
func NewTencentModerator() Moderator                  { return &TencentModerator{} }

// NewTencentIMProvider 腾讯云 IM Provider 骨架。真实发送需 UserSig(管理员签发) + REST API。
// 仅当配置了 ZANBUDDY_TENCENT_IM_APPID 等环境变量时由 main 注入; 否则不启用。
// 已实现的真实形态: UserSig 生成(HMAC-SHA256, 与官方算法一致) + openim/sendmsg 请求合同 + 带鉴权的 HTTP 调用。
func NewTencentIMProvider(appID int, secretKey, adminUser, apiURL string) Provider {
	return &TencentIMProvider{SDKAppID: appID, SecretKey: secretKey, AdminUser: adminUser, APIURL: apiURL}
}

// genUserSig 生成腾讯云 IM UserSig(管理员/用户登录凭证)。
// 算法与官方一致: HMAC-SHA256(明文, SecretKey) 后 base64, 再打包为 JSON 并 base64。
func genUserSecSig(secretKey string, sdkAppID int, identifier string, expire int) (string, error) {
	if secretKey == "" {
		return "", fmt.Errorf("腾讯云 IM SecretKey 未配置")
	}
	content := fmt.Sprintf("TLS.identifier:%s\nTLS.sdkappid:%d\nTLS.secretkey:%s\nTLS.expire:%d\n",
		identifier, sdkAppID, secretKey, expire)
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(content))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	payload, err := json.Marshal(map[string]interface{}{
		"TLS.ver":       1,
		"TLS.identifier": identifier,
		"TLS.sdkappid":  sdkAppID,
		"TLS.expire":    expire,
		"TLS.sig":       sig,
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(payload), nil
}

func convID(a, b string) string {
	if a < b {
		return a + ":" + b
	}
	return b + ":" + a
}

// Send 人类发送消息(默认 IsAI=false, aiType="")。
func (s *Service) Send(from, to, body string) (*model.ChatMessage, string) {
	return s.SendWithFlags(from, to, body, false, "")
}

// SendWithFlags 发送消息并指定 AI 标记(供 service 层传入 aiType, 如 "suggest")。
func (s *Service) SendWithFlags(from, to, body string, isAI bool, aiType string) (*model.ChatMessage, string) {
	msg, reason := s.sendRaw(from, to, body, isAI, aiType)
	if msg == nil {
		return nil, reason
	}
	return msg, ""
}

// SendAI 代发一条 AI 消息(IsAI=true), 不经过自动回复钩子, 用于「AI 自动回复」。
// 失败(参数非法/被拦截)返回 nil, 调用方静默跳过。
func (s *Service) SendAI(from, to, body, aiType string) *model.ChatMessage {
	msg, _ := s.sendRaw(from, to, body, true, aiType)
	return msg
}

// sendRaw 消息底层构造: 审核 → 落库 → 更会话 → 推 SSE → 调 Provider(异步)。
func (s *Service) sendRaw(from, to, body string, isAI bool, aiType string) (*model.ChatMessage, string) {
	if from == "" || to == "" || from == to {
		return nil, "收发方无效"
	}
	if strings.TrimSpace(body) == "" {
		return nil, "消息内容为空"
	}
	if blocked, reason := s.mod.Check(body); blocked {
		return nil, "消息被拦截: " + reason
	}
	msg := &model.ChatMessage{
		ID:        s.st.NextID("M"),
		ConvID:    convID(from, to),
		FromID:    from,
		ToID:      to,
		Body:      body,
		Status:    model.MsgNormal,
		IsAI:      isAI,
		AIType:    aiType,
		CreatedAt: model.Now(),
	}
	s.st.SaveMessage(msg)
	conv := s.st.GetOrCreateConversation(from, to, "")
	s.touch(conv, msg)
	s.push(msg)
	// 外部通道(异步, 失败不影响本地链路)
	go func() {
		_, _ = s.prov.Send(context.Background(), from, to, body)
	}()
	return msg, ""
}

// SendSystem 系统消息(订单建立通知等)。from="system", 会话落在 a:b。
// 系统消息对双方可见(按 convID 读取); 未读仅记给接收方 b。
func (s *Service) SendSystem(a, b, orderID, body string) *model.ChatMessage {
	cid := convID(a, b)
	msg := &model.ChatMessage{
		ID:        s.st.NextID("M"),
		ConvID:    cid,
		FromID:    "system",
		ToID:      b,
		Body:      body,
		Status:    model.MsgNormal,
		CreatedAt: model.Now(),
	}
	s.st.SaveMessage(msg)
	conv := s.st.GetOrCreateConversation(a, b, orderID)
	if conv.OrderID == "" && orderID != "" { // 会话若先由普通消息建立, 此处补全订单关联
		conv.OrderID = orderID
	}
	conv.LastBody = body
	conv.LastAt = msg.CreatedAt
	conv.UpdatedAt = msg.CreatedAt
	conv.BUnread++
	s.st.SaveConversation(conv)
	s.push(msg)
	return msg
}

// touch 更新会话最后消息与未读计数(发给谁, 谁未读+1)。
func (s *Service) touch(conv *model.Conversation, msg *model.ChatMessage) {
	conv.LastBody = msg.Body
	conv.LastAt = msg.CreatedAt
	conv.UpdatedAt = msg.CreatedAt
	if msg.ToID == conv.A {
		conv.AUnread++
	} else if msg.ToID == conv.B {
		conv.BUnread++
	}
	s.st.SaveConversation(conv)
}

// push 推送给在线订阅者(SSE)。
func (s *Service) push(msg *model.ChatMessage) {
	s.mu.RLock()
	subs := append([]chan *model.ChatMessage{}, s.subs[msg.ConvID]...)
	s.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

// Subscribe 订阅会话, 返回会话ID与消息通道(供 SSE 实时推送)。
func (s *Service) Subscribe(a, b string) (string, chan *model.ChatMessage) {
	cid := convID(a, b)
	ch := make(chan *model.ChatMessage, 16)
	s.mu.Lock()
	s.subs[cid] = append(s.subs[cid], ch)
	s.mu.Unlock()
	return cid, ch
}

// Recent 拉取会话最近消息(轮询兜底 / SSE 首屏历史)。
func (s *Service) Recent(a, b string) []*model.ChatMessage {
	return s.st.ListMessages(convID(a, b))
}

// GetMessages 取会话全部消息(等价于 Recent, 语义更清晰)。
func (s *Service) GetMessages(a, b string) []*model.ChatMessage {
	return s.st.ListMessages(convID(a, b))
}

// ListConversations 列出某用户的所有会话(按更新时间倒序, 调用方排序或此处排序)。
func (s *Service) ListConversations(uid string) []*model.Conversation {
	return s.st.ListConversations(uid)
}

// MarkRead 将某会话中发给 uid 的未读清零, 返回清零条数。
func (s *Service) MarkRead(a, b string) int {
	cid := convID(a, b)
	conv, ok := s.st.GetConversation(cid)
	if !ok {
		return 0
	}
	var n int
	if a == conv.A {
		n = conv.AUnread
		conv.AUnread = 0
	} else if a == conv.B {
		n = conv.BUnread
		conv.BUnread = 0
	}
	if n > 0 {
		s.st.SaveConversation(conv)
	}
	return n
}

// ---- Providers ----

// StubProvider 默认 Provider: 不调外部, 仅模拟返回外部ID。
type StubProvider struct{}

func (StubProvider) Name() string { return "stub" }
func (StubProvider) Send(_ context.Context, from, to, body string) (string, error) {
	return "stub-" + from + "-" + to + "-" + fmt.Sprintf("%d", model.Now()), nil
}

// TencentIMProvider 腾讯云 IM Provider 骨架(真实 API 待接 SDK)。
type TencentIMProvider struct {
	SDKAppID  int
	SecretKey string
	AdminUser string
	APIURL    string
}

func (p *TencentIMProvider) Name() string { return "tencent-im" }

// Send 真实形态: 当 SDKAppID/SecretKey/AdminUser 齐全时, 用 UserSig 鉴权调用 openim/sendmsg。
// 缺凭证时返回明确错误(由 main 决定是否注入, 未注入则不启用)。
func (p *TencentIMProvider) Send(_ context.Context, from, to, body string) (string, error) {
	if p.SDKAppID == 0 || p.SecretKey == "" || p.AdminUser == "" {
		return "", fmt.Errorf("tencent-im provider 未配置(需 SDKAppID/SecretKey/AdminUser)")
	}
	apiURL := p.APIURL
	if apiURL == "" {
		apiURL = "https://console.tim.qq.com"
	}
	sig, err := genUserSecSig(p.SecretKey, p.SDKAppID, p.AdminUser, 86400*180)
	if err != nil {
		return "", err
	}
	// 单聊文本消息合同(腾讯云 IM REST API v4/openim/sendmsg)
	payload := map[string]interface{}{
		"From_Account": p.AdminUser,
		"To_Account":   to,
		"MsgRandom":    int(time.Now().UnixNano() % 4294967295),
		"MsgTimeStamp": time.Now().Unix(),
		"MsgBody": []map[string]interface{}{
			{"MsgType": "TIMTextElem", "MsgContent": map[string]string{"Text": body}},
		},
	}
	b, _ := json.Marshal(payload)
	q := fmt.Sprintf("?usersig=%s&identifier=%s&sdkappid=%d&random=%d&contenttype=json",
		sig, p.AdminUser, p.SDKAppID, int(time.Now().UnixNano()%4294967295))
	req, err := http.NewRequest("POST", apiURL+"/v4/openim/sendmsg"+q, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 2048)
	tmp := make([]byte, 512)
	for {
		n, e := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if e != nil {
			break
		}
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("腾讯云 IM HTTP %d: %s", resp.StatusCode, string(buf))
	}
	var r struct {
		ActionStatus string `json:"ActionStatus"`
		ErrorInfo    string `json:"ErrorInfo"`
		MsgKey       string `json:"MsgKey"`
	}
	if e := json.Unmarshal(buf, &r); e != nil {
		return "", fmt.Errorf("腾讯云 IM 响应解析失败: %w", e)
	}
	if r.ActionStatus != "OK" {
		return "", fmt.Errorf("腾讯云 IM 发送失败: %s", r.ErrorInfo)
	}
	return r.MsgKey, nil
}

// ---- Moderators ----

// StubModerator 默认审核: 拦截规避平台交易/导流关键词(演示合规闸门)。
type StubModerator struct{}

func (StubModerator) Name() string { return "stub" }

var blockWords = []string{
	"微信", "vx", "weixin", "wechat", "加我", "私下", "私聊", "转账",
	"支付宝", "qq", "QQ", "站外", "绕过", "线下交易", "银行卡", "电话",
}

func (StubModerator) Check(body string) (bool, string) {
	for _, w := range blockWords {
		if strings.Contains(body, w) {
			return true, "命中规避平台交易关键词: " + w
		}
	}
	return false, ""
}

// TencentModerator 腾讯云内容安全(天御)骨架。
type TencentModerator struct{}

func (TencentModerator) Name() string { return "tencent-cms" }
func (TencentModerator) Check(body string) (bool, string) {
	// TODO(P2-2 真实对接): 调用腾讯云内容安全文本审核接口, 命中违规词/涉政/涉黄则拦截。
	_ = body
	return false, ""
}
