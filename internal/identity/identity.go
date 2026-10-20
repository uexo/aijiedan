// Package identity 身份域: 手机验证码登录、会话、供给方档案。
//
// 为什么不用密码: 这个产品没有支付密码、没有会员密码, 也没有需要保护的订阅资产。
// 多一个密码只多一个找回流程, 而手机号本身就是账号。
//
// 为什么验证码放内存: 单实例部署, 验证码 5 分钟过期, 重启丢失可接受(用户重新发一次)。
// 将来多实例时换成 Redis, SendCode/Verify 两个方法签名不变。
package identity

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"zanbuddy/internal/middleware"
	"zanbuddy/internal/model"
	"zanbuddy/internal/store"
)

// 业务错误。handler 层按 code 映射 HTTP 状态, 不在这一层关心状态码。
var (
	ErrInvalidPhone = errors.New("手机号格式不对")
	ErrCodeExpired  = errors.New("验证码已过期，请重新获取")
	ErrCodeWrong    = errors.New("验证码不对")
	ErrTooManyTry   = errors.New("验证次数太多，请等一分钟再试")
	ErrUnauthorized = errors.New("请先登录")
	ErrSkillsRange  = errors.New("技能标签请选 3 到 5 个")
)

const (
	codeTTL      = 5 * time.Minute
	codeMaxTries = 5
	// devCode 是开发环境的固定验证码。生产必须关掉(SMS 通道接进来后 SendCode 不再返回验证码)。
	devCode = "000000"
)

type codeEntry struct {
	code     string
	expireAt time.Time
	tries    int
}

// Service 身份域服务。
type Service struct {
	st store.Store

	mu    sync.Mutex
	codes map[string]*codeEntry
	// devMode 为真时 SendCode 直接返回验证码, 不接短信通道。
	devMode bool
}

func New(st store.Store, devMode bool) *Service {
	return &Service{st: st, codes: map[string]*codeEntry{}, devMode: devMode}
}

// NormalizePhone 去掉空格与 +86 前缀, 校验 11 位手机号。
// 只做形状校验, 不做运营商段校验——170/171 这类虚拟号段也是真实用户。
func NormalizePhone(raw string) (string, error) {
	p := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(raw))
	p = strings.TrimPrefix(p, "+86")
	p = strings.TrimPrefix(p, "86")
	if len(p) != 11 || p[0] != '1' {
		return "", ErrInvalidPhone
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return "", ErrInvalidPhone
		}
	}
	return p, nil
}

// SendCode 生成并保存验证码。返回验证码只在 devMode 下非空;
// 生产接短信通道后返回空串, 由通道下发。
func (s *Service) SendCode(phone string) (string, error) {
	if _, err := NormalizePhone(phone); err != nil {
		return "", err
	}
	code := devCode
	if !s.devMode {
		code = randomDigits(6)
	}
	s.mu.Lock()
	s.codes[phone] = &codeEntry{code: code, expireAt: time.Now().Add(codeTTL)}
	s.mu.Unlock()
	if s.devMode {
		return code, nil
	}
	return "", nil
}

// verify 校验验证码。失败计数超过 5 次即作废该码, 防止 6 位空间被穷举。
func (s *Service) verify(phone, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.codes[phone]
	if !ok || time.Now().After(e.expireAt) {
		return ErrCodeExpired
	}
	if e.tries >= codeMaxTries {
		delete(s.codes, phone)
		return ErrTooManyTry
	}
	if e.code != code {
		e.tries++
		return ErrCodeWrong
	}
	delete(s.codes, phone)
	return nil
}

// LoginResult 登录返回: 用户、会话 token、是否新注册。
type LoginResult struct {
	User    *model.User `json:"user"`
	Token   string      `json:"token"`
	IsNew   bool        `json:"is_new"`
	Onboard bool        `json:"onboarded"`
}

// LoginByCode 验证码登录。手机号不存在就注册, 身份取第一次登录时选的倾向。
// role 只在注册时生效, 老用户传什么都按库里已有的角色, 避免前端一次传错就改了权限。
func (s *Service) LoginByCode(phone, code, role string) (*LoginResult, error) {
	p, err := NormalizePhone(phone)
	if err != nil {
		return nil, err
	}
	if err := s.verify(p, code); err != nil {
		return nil, err
	}
	isNew := false
	u, ok := s.st.GetUserByPhone(p)
	if !ok {
		isNew = true
		u = s.newUser(p, role)
	}
	tok, err := middleware.Sign(u.ID, string(u.Role))
	if err != nil {
		return nil, err
	}
	return &LoginResult{User: u, Token: tok, IsNew: isNew, Onboard: u.Onboarded}, nil
}

func (s *Service) newUser(phone, role string) *model.User {
	id := s.st.NextID("U")
	u := &model.User{
		ID:          id,
		Account:     phone,
		Phone:       phone,
		Name:        "用户" + id,
		Role:        normalizeRole(role),
		Slug:        s.newSlug(),
		CreditScore: 50,
		CreatedAt:   model.Now(),
	}
	s.st.PutUser(u)
	return u
}

// normalizeRole 对齐两套角色口径。登录按钮上写的是 supply / demand,
// 库里存的是 provider / requester。不转这一下, 选了「接单方」的人会被注册成需求方,
// 表现就是"登录完进不了大厅、还要先填档案却没人让他填"。
func normalizeRole(raw string) model.Role {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "provider", "supply", "taker", "接单", "接单方":
		return model.RoleProvider
	case "requester", "demand", "buyer", "需求", "需求方":
		return model.RoleRequester
	}
	return model.RoleRequester
}

// slugAlphabet 去掉 l / o / 0 / 1: 短号会被人手抄、口头念, 形近字符是真实成本。
const slugAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// newSlug 生成 6 位公开短号。32^6 约十亿种, 这个量级不会撞;
// 仍然查一次库是因为撞了会写坏唯一索引, 报错比多一次查询贵。
func (s *Service) newSlug() string {
	for i := 0; i < 8; i++ {
		b := make([]byte, 6)
		for j := range b {
			v, err := rand.Int(rand.Reader, big.NewInt(int64(len(slugAlphabet))))
			if err != nil {
				continue
			}
			b[j] = slugAlphabet[v.Int64()]
		}
		slug := string(b)
		if _, exists := s.st.GetUserBySlug(slug); !exists {
			return slug
		}
	}
	return "u" + randomDigits(7)
}

// Me 当前用户 + 档案(供给方才有)。档案不存在时返回空档案而不是 nil,
// 前端不用为"第一次进来还没有档案"写一套分支。
func (s *Service) Me(uid string) (*model.User, *model.ProviderProfile, error) {
	u, ok := s.st.GetUser(uid)
	if !ok {
		return nil, nil, ErrUnauthorized
	}
	if p, ok := s.st.GetProfile(uid); ok {
		return u, p, nil
	}
	return u, &model.ProviderProfile{UserID: uid, Availability: 1}, nil
}

// ProfileInput 档案保存入参。指针用于区分"没传这个字段"和"传了空值":
// 只传 headline 时不该把 bio 清空。
type ProfileInput struct {
	Headline     *string              `json:"headline"`
	Bio          *string              `json:"bio"`
	Skills       *[]string            `json:"skills"`
	Portfolio    *[]model.PortfolioItem `json:"portfolio"`
	Availability *int                 `json:"availability"`
}

// SaveProfile 全量或部分保存档案, 返回落库后的结果(含重算的完整度)。
func (s *Service) SaveProfile(uid string, in ProfileInput) (*model.ProviderProfile, error) {
	u, ok := s.st.GetUser(uid)
	if !ok {
		return nil, ErrUnauthorized
	}
	p, ok := s.st.GetProfile(uid)
	if !ok {
		p = &model.ProviderProfile{UserID: uid, Availability: 1}
	}
	if in.Headline != nil {
		p.Headline = strings.TrimSpace(*in.Headline)
		if len([]rune(p.Headline)) > 60 {
			p.Headline = string([]rune(p.Headline)[:60])
		}
	}
	if in.Bio != nil {
		p.Bio = strings.TrimSpace(*in.Bio)
		if len([]rune(p.Bio)) > 200 {
			p.Bio = string([]rune(p.Bio)[:200])
		}
	}
	if in.Skills != nil {
		p.Skills = dedupe(*in.Skills)
	}
	if in.Portfolio != nil {
		p.Portfolio = cleanPortfolio(*in.Portfolio)
	}
	if in.Availability != nil {
		if *in.Availability == 0 {
			p.Availability = 0
		} else {
			p.Availability = 1
		}
	}
	// 技能数量是硬规则不是建议: 超过 5 个, 推荐理由会退化成"技能匹配度高"这种不可指认的废话。
	if len(p.Skills) > 0 && (len(p.Skills) < 3 || len(p.Skills) > 5) {
		return nil, ErrSkillsRange
	}
	p.Completeness = Completeness(p)
	p.UpdatedAt = model.Now()
	s.st.PutProfile(p)

	// 档案有内容就算走完引导, 进召回池。headline 是硬门槛: 卡片第二行没有它就没法解释为什么推荐这个人。
	if p.Headline != "" && !u.Onboarded {
		u.Onboarded = true
		s.st.PutUser(u)
	}
	return p, nil
}

// Completeness 档案完整度 0-100。算法与前端 profile.js 必须一致, 改动要两边同时改。
// headline 20 + 技能≥3 个 30 + 作品≥1 条 20 + bio 15 + 可接单 15。
func Completeness(p *model.ProviderProfile) int {
	score := 0
	if strings.TrimSpace(p.Headline) != "" {
		score += 20
	}
	if len(p.Skills) >= 3 {
		score += 30
	}
	if len(p.Portfolio) >= 1 {
		score += 20
	}
	if strings.TrimSpace(p.Bio) != "" {
		score += 15
	}
	if p.Availability == 1 {
		score += 15
	}
	return score
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			continue
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	return out
}

// cleanPortfolio 丢掉空行, 最多留 3 条。空行不是错误输入, 是用户点了"再加一条"还没填。
func cleanPortfolio(in []model.PortfolioItem) []model.PortfolioItem {
	out := make([]model.PortfolioItem, 0, 3)
	for _, it := range in {
		it.Label = strings.TrimSpace(it.Label)
		it.URL = strings.TrimSpace(it.URL)
		if it.URL == "" {
			continue
		}
		out = append(out, it)
		if len(out) == 3 {
			break
		}
	}
	return out
}

func randomDigits(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		v, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			b.WriteByte('0')
			continue
		}
		b.WriteString(fmt.Sprint(v.Int64()))
	}
	return b.String()
}
