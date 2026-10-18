// Package kyc 实名认证(KYC)通道层。
// 与 payment 包同构: 抽象两种通道 ——
//   - MockProvider: 离线模拟(默认), dev 自动通过, 无真实 KYC 厂商资质即可演示与冒烟。
//   - RealProvider: 真实 KYC 厂商(如阿里云/腾讯云实名认证)骨架, 仅当 LIANVO_KYC_APIKEY 齐全时 live。
//
// 合规闸门(compliance)通过 SetKYCProvider 注入 Status 钩子, 使 KYC 从纯状态位(u.KYCVerified)
// 改为「可查询 Provider 状态」, 真实模式下由厂商回调/轮询刷新 u.KYCVerified。
package kyc

import (
	"fmt"
	"os"
)

// Provider 实名认证通道抽象。
type Provider interface {
	// Submit 提交实名认证(姓名+证件号), 返回受理单号。
	Submit(userID, name, idNo string) (ref string, err error)
	// Status 查询认证状态: (是否已通过, 状态文本, 错误)。
	Status(userID string) (verified bool, state string, err error)
}

// Service 对外门面。
type Service struct {
	prov Provider
}

// New 依环境变量选择通道: LIANVO_KYC_PROVIDER=="real" 且凭证齐全走真实厂商, 否则 Mock。
func New() *Service {
	return &Service{prov: NewProvider()}
}

// Submit 透传通道: 提交实名认证, 返回受理单号。
func (s *Service) Submit(userID, name, idNo string) (string, error) {
	return s.prov.Submit(userID, name, idNo)
}

// Status 透传通道: 查询认证状态(是否已通过, 状态文本, 错误)。
func (s *Service) Status(userID string) (bool, string, error) {
	return s.prov.Status(userID)
}

// Readiness 返回 KYC 通道的生产就绪态(供 /api/v1/readiness 与运营「上线就绪」面板)。
// 仅在显式选择真实厂商(LIANVO_KYC_PROVIDER=real)时校验凭证; 否则为 mock 演示模式。
func (s *Service) Readiness() (mode string, live bool, missing []string) {
	p, ok := s.prov.(*RealProvider)
	if !ok {
		return "mock", false, nil
	}
	if p.APIKey == "" {
		missing = append(missing, "LIANVO_KYC_APIKEY")
	}
	if p.BaseURL == "" {
		missing = append(missing, "LIANVO_KYC_BASEURL")
	}
	return "real", len(missing) == 0, missing
}

// NewProvider 构造通道实现。
func NewProvider() Provider {
	if os.Getenv("LIANVO_KYC_PROVIDER") == "real" {
		p := &RealProvider{
			APIKey:  os.Getenv("LIANVO_KYC_APIKEY"),
			BaseURL: os.Getenv("LIANVO_KYC_BASEURL"),
		}
		p.live = p.APIKey != ""
		if !p.live {
			fmt.Println("[kyc] 已选真实通道但 LIANVO_KYC_APIKEY 未配置, 退回 pending 受理(不自动通过)")
		}
		return p
	}
	return &MockProvider{}
}

// MockProvider 模拟通道: dev 自动通过。
type MockProvider struct{}

func (p *MockProvider) Submit(userID, name, idNo string) (string, error) {
	return "MOCKKYC_" + userID, nil
}
func (p *MockProvider) Status(userID string) (bool, string, error) {
	return true, "passed", nil
}

// RealProvider 真实 KYC 厂商骨架。
type RealProvider struct {
	APIKey  string
	BaseURL string
	live    bool
}

// Submit 真实模式下应 POST 到 KYC 厂商提交实名; 此处为骨架。
func (p *RealProvider) Submit(userID, name, idNo string) (string, error) {
	if !p.live {
		return "KYCPEND_" + userID, nil
	}
	// TODO(real): POST {BaseURL}/v1/verify {user_id,name,id_no} -> 受理单号
	return "KYC_" + userID, nil
}

// Status 真实模式下应查询厂商认证结果; 此处为骨架。
func (p *RealProvider) Status(userID string) (bool, string, error) {
	if !p.live {
		return false, "pending", nil
	}
	// TODO(real): GET {BaseURL}/v1/verify/{user_id} -> verified
	return false, "pending", nil
}
