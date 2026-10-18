// Package payment 资金通道层。
// 红线: 严禁平台先收后付的「二清/资金池」模式, 必须经持牌通道(微信支付)完成冻结/解冻/分账。
//
// Provider 抽象两种通道:
//   - StubProvider: 离线模拟(默认), 演示/测试无商户资质可用, 不接真实资金。
//   - WeChatProvider: 微信支付「担保交易(JSAPI 下单, 资金由微信侧冻结/持有) + 分账(Release) + 退款(Refund)」真实形态骨架。
//     已实现: 真实 API 端点、请求/响应结构体、v3 RSA-SHA256 签名与 Authorization 头、私钥加载。
//     仅当 ZANBUDDY_WECHAT_MCHID/APPID/SERIAL/KEY_PEM 四项齐全时 live=true 才真正发起 HTTP 请求;
//     缺任一则退回 stub(生成单号/打印), 不影响演示与冒烟。
//
// Service 为对外门面, 保持 Escrow/Release/Refund/CommissionCents 签名不变, 上层 service 调用点零改动。
// 平台内记账(钱包/流水)由 service 层负责, 不在此处。
package payment

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Provider 资金通道抽象: 真实资金经此进出, 平台不沉淀。
type Provider interface {
	Escrow(orderID string, amount int64) (string, error)        // 担保交易下单并冻结, 返回托管单号(prepay_id / out_trade_no)
	Release(escrowID string, toProvider, toPlatform int64) error // 验收后分账(接单方 + 平台佣金)
	Refund(escrowID string, toRequester int64) error            // 退款(原路退回)
	isLive() bool                                                // 是否真实通道(决定同步记账 or 等异步回调)
	Notify(body []byte) (orderID string, ok bool)                // 支付结果回调: 解析出订单号并判断是否支付成功
}

// Service 对外门面, 内部委托 Provider。
type Service struct {
	prov Provider
}

// New 依环境变量选择通道: 微信凭证齐全走微信真实形态, 否则 Stub。
func New() *Service {
	return &Service{prov: NewProvider()}
}

// NewProvider 构造通道实现。
func NewProvider() Provider {
	mch := os.Getenv("ZANBUDDY_WECHAT_MCHID")
	if mch != "" {
		p := &WeChatProvider{
			MchID:         mch,
			AppID:         os.Getenv("ZANBUDDY_WECHAT_APPID"),
			SerialNo:      os.Getenv("ZANBUDDY_WECHAT_SERIAL"),
			PrivateKeyPEM: os.Getenv("ZANBUDDY_WECHAT_KEY_PEM"),
			NotifyURL:     os.Getenv("ZANBUDDY_WECHAT_NOTIFY"),
			APIV3Key:      os.Getenv("ZANBUDDY_WECHAT_APIV3_KEY"),
		}
		p.live = p.AppID != "" && p.SerialNo != "" && p.PrivateKeyPEM != ""
		if !p.live {
			fmt.Println("[payment] 微信商户号已设但凭证不全(AppID/Serial/KeyPEM), 退回 stub 形态")
		}
		return p
	}
	return &StubProvider{}
}

// Escrow 冻结需求方资金, 返回托管单号。
func (s *Service) Escrow(orderID string, amount int64) (string, error) {
	return s.prov.Escrow(orderID, amount)
}

// Release 验收后释放: 接单方 + 平台佣金自动分账。
func (s *Service) Release(escrowID string, toProvider, toPlatform int64) error {
	return s.prov.Release(escrowID, toProvider, toPlatform)
}

// Refund 纠纷/取消原路退款。
func (s *Service) Refund(escrowID string, toRequester int64) error {
	return s.prov.Refund(escrowID, toRequester)
}

// IsLive 是否真实资金通道。Service 层据此决定: 真实通道下 CreateEscrow 仅冻结并等待
// 异步支付回调(订单停在 pending_escrow); 模拟通道下同步记为已托管(订单直接 active)。
func (s *Service) IsLive() bool { return s.prov.isLive() }

// HandleNotify 接收支付通道的异步结果回调(微信支付结果通知等), 解析出订单号与支付成败。
// 具体验签/解密由 Provider 实现; Service 层只关心「这笔订单是否支付成功」。
func (s *Service) HandleNotify(body []byte) (orderID string, ok bool) {
	return s.prov.Notify(body)
}

// CommissionCents 平台佣金(10%)。金额单位: 分。
func CommissionCents(amount int64) int64 { return amount * 10 / 100 }

// StubProvider 离线模拟通道: 不接真实资金, 仅生成单号/打印, 供演示与冒烟。
type StubProvider struct{}

func (p *StubProvider) Escrow(orderID string, amount int64) (string, error) {
	return "ESCROW_" + orderID, nil
}
func (p *StubProvider) Release(escrowID string, toProvider, toPlatform int64) error {
	fmt.Printf("[payment.stub] release escrow=%s provider=%d platform=%d\n", escrowID, toProvider, toPlatform)
	return nil
}
func (p *StubProvider) Refund(escrowID string, toRequester int64) error {
	fmt.Printf("[payment.stub] refund escrow=%s requester=%d\n", escrowID, toRequester)
	return nil
}
func (p *StubProvider) isLive() bool { return false }
// Notify 模拟通道的回调约定: 收到 JSON {"order_id":"...","success":true} 即视为支付成功。
func (p *StubProvider) Notify(body []byte) (string, bool) {
	var n struct {
		OrderID string `json:"order_id"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal(body, &n); err != nil || n.OrderID == "" {
		return "", false
	}
	return n.OrderID, n.Success
}

// ---- 微信支付 v3 真实形态骨架 ----

const (
	wxBaseURL    = "https://api.mch.weixin.qq.com"
	wxJSAPIPath  = "/v3/pay/transactions/jsapi"   // 担保交易下单(资金由微信冻结/持有)
	wxSharePath  = "/v3/profitsharing/orders"     // 分账(接单方 + 平台佣金, 平台不沉淀)
	wxRefundPath = "/v3/refund/domestic/refunds"  // 退款
)

// 微信支付 v3 请求/响应结构体(合同形态, 与官方字段对齐)。
type wxJSAPIReq struct {
	Mchid       string    `json:"mchid"`
	Appid       string    `json:"appid"`
	Description string    `json:"description"`
	OutTradeNo  string    `json:"out_trade_no"`
	NotifyURL   string    `json:"notify_url"`
	Amount      wxAmount  `json:"amount"`
	Payer       wxPayer   `json:"payer"`
}
type wxAmount struct {
	Total    int64  `json:"total"` // 单位: 分
	Currency string `json:"currency"`
}
type wxPayer struct {
	Openid string `json:"openid"`
}
type wxJSAPIRsp struct {
	PrepayID string `json:"prepay_id"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type wxReceiver struct {
	Type        string `json:"type"` // MERCHANT_ID / PERSONAL_OPENID
	Account     string `json:"account"`
	Amount      int64  `json:"amount"` // 分
	Description string `json:"description"`
}
type wxShareReq struct {
	Appid      string        `json:"appid"`
	OutTradeNo string        `json:"out_trade_no"`
	Receivers  []wxReceiver  `json:"receivers"`
}
type wxShareRsp struct {
	OutTradeNo string `json:"out_trade_no"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type wxRefundReq struct {
	OutTradeNo  string `json:"out_trade_no"`
	OutRefundNo string `json:"out_refund_no"`
	Reason      string `json:"reason"`
	Amount      struct {
		Refund   int64  `json:"refund"`
		Total    int64  `json:"total"`
		Currency string `json:"currency"`
	} `json:"amount"`
}
type wxRefundRsp struct {
	OutRefundNo string `json:"out_refund_no"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}

// WeChatProvider 微信支付 v3 真实形态骨架。
// live=false 时退化为 stub(生成单号/打印); live=true 时发起真实 HTTPS 请求(需私钥与商户证书齐全)。
type WeChatProvider struct {
	MchID         string
	AppID         string
	SerialNo      string
	PrivateKeyPEM string
	APIV3Key      string // 微信支付 APIv3 密钥, 用于回调报文 AES-256-GCM 解密
	NotifyURL     string
	live          bool
	key           *rsa.PrivateKey // 懒加载缓存
}

// privateKey 解析商户私钥(PKCS8 / PKCS1 均兼容)。
func (p *WeChatProvider) privateKey() (*rsa.PrivateKey, error) {
	if p.key != nil {
		return p.key, nil
	}
	if p.PrivateKeyPEM == "" {
		return nil, fmt.Errorf("微信支付私钥未配置(ZANBUDDY_WECHAT_KEY_PEM)")
	}
	block, _ := pem.Decode([]byte(p.PrivateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("微信支付私钥 PEM 解析失败")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			p.key = rk
			return p.key, nil
		}
	}
	if rk, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		p.key = rk
		return p.key, nil
	}
	return nil, fmt.Errorf("微信支付私钥解析失败(非 RSA 私钥)")
}

// genNonce 生成 32 位十六进制随机串。
func genNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// genAuth 生成 v3 Authorization 头(RSA-SHA256 签名)。
func (p *WeChatProvider) genAuth(method, path, body string, ts int64, nonce string) (string, error) {
	key, err := p.privateKey()
	if err != nil {
		return "", err
	}
	plain := method + "\n" + path + "\n" + strconv.FormatInt(ts, 10) + "\n" + nonce + "\n" + body + "\n"
	h := sha256.Sum256([]byte(plain))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",signature="%s",timestamp="%d",serial_no="%s"`,
		p.MchID, nonce, base64.StdEncoding.EncodeToString(sig), ts, p.SerialNo), nil
}

// doPost 发起带 v3 签名头的 POST 请求, 返回响应体。
func (p *WeChatProvider) doPost(path, body string) ([]byte, error) {
	ts := time.Now().Unix()
	nonce := genNonce()
	auth, err := p.genAuth("POST", path, body, ts, nonce)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", wxBaseURL+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 512)
	for {
		n, e := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if e != nil {
			break
		}
	}
	if resp.StatusCode != 200 {
		return buf, fmt.Errorf("微信支付 HTTP %d: %s", resp.StatusCode, string(buf))
	}
	return buf, nil
}

// Escrow 担保交易下单(资金由微信侧冻结/持有), 返回 prepay_id 作为托管单号。
func (p *WeChatProvider) Escrow(orderID string, amount int64) (string, error) {
	if !p.live {
		fmt.Printf("[payment.wechat] escrow(order=%s amount=%d) stub (未配置完整凭证)\n", orderID, amount)
		return "WXESCROW_" + orderID, nil
	}
	req := wxJSAPIReq{
		Mchid:       p.MchID,
		Appid:       p.AppID,
		Description: "Zanbuddy 任务托管 " + orderID,
		OutTradeNo:  orderID,
		NotifyURL:   p.NotifyURL,
		Amount:      wxAmount{Total: amount, Currency: "CNY"},
		Payer:       wxPayer{Openid: ""}, // 生产由前端 JSAPI 拉起支付时补 openid
	}
	b, _ := json.Marshal(req)
	data, err := p.doPost(wxJSAPIPath, string(b))
	if err != nil {
		return "", err
	}
	var r wxJSAPIRsp
	if e := json.Unmarshal(data, &r); e != nil {
		return "", fmt.Errorf("微信下单响应解析失败: %w", e)
	}
	if r.PrepayID == "" {
		return "", fmt.Errorf("微信下单失败: %s %s", r.Code, r.Message)
	}
	return r.PrepayID, nil
}

// Release 验收后分账: 接单方(PERSONAL_OPENID) + 平台佣金(MERCHANT_ID)。平台不沉淀资金。
func (p *WeChatProvider) Release(escrowID string, toProvider, toPlatform int64) error {
	if !p.live {
		fmt.Printf("[payment.wechat] release(escrow=%s provider=%d platform=%d) stub (未配置完整凭证)\n", escrowID, toProvider, toPlatform)
		return nil
	}
	req := wxShareReq{
		Appid:      p.AppID,
		OutTradeNo: escrowID,
		Receivers: []wxReceiver{
			{Type: "PERSONAL_OPENID", Account: "${provider_openid}", Amount: toProvider, Description: "接单方结算"},
			{Type: "MERCHANT_ID", Account: p.MchID, Amount: toPlatform, Description: "平台佣金"},
		},
	}
	b, _ := json.Marshal(req)
	data, err := p.doPost(wxSharePath, string(b))
	if err != nil {
		return err
	}
	var r wxShareRsp
	if e := json.Unmarshal(data, &r); e != nil {
		return fmt.Errorf("微信分账响应解析失败: %w", e)
	}
	if r.OutTradeNo == "" {
		return fmt.Errorf("微信分账失败: %s %s", r.Code, r.Message)
	}
	return nil
}

// Refund 退款(原路退回)。
func (p *WeChatProvider) Refund(escrowID string, toRequester int64) error {
	if !p.live {
		fmt.Printf("[payment.wechat] refund(escrow=%s requester=%d) stub (未配置完整凭证)\n", escrowID, toRequester)
		return nil
	}
	req := wxRefundReq{
		OutTradeNo:  escrowID,
		OutRefundNo: "RF_" + escrowID,
		Reason:      "订单退款/纠纷",
	}
	req.Amount.Refund = toRequester
	req.Amount.Total = toRequester
	req.Amount.Currency = "CNY"
	b, _ := json.Marshal(req)
	data, err := p.doPost(wxRefundPath, string(b))
	if err != nil {
		return err
	}
	var r wxRefundRsp
	if e := json.Unmarshal(data, &r); e != nil {
		return fmt.Errorf("微信退款响应解析失败: %w", e)
	}
	if r.OutRefundNo == "" {
		return fmt.Errorf("微信退款失败: %s %s", r.Code, r.Message)
	}
	return nil
}

// isLive 真实通道标志。
func (p *WeChatProvider) isLive() bool { return p.live }

// Readiness 返回资金通道的生产就绪态(供 /api/v1/readiness 与运营「上线就绪」面板)。
// 仅在商户号已设定(即选了微信通道)时校验其余凭证; 未设商户号视为 stub 演示模式。
func (s *Service) Readiness() (mode string, live bool, missing []string) {
	p, ok := s.prov.(*WeChatProvider)
	if !ok {
		return "stub", false, nil
	}
	if p.MchID == "" {
		return "stub", false, nil
	}
	mode = "wechat"
	if p.AppID == "" {
		missing = append(missing, "ZANBUDDY_WECHAT_APPID")
	}
	if p.SerialNo == "" {
		missing = append(missing, "ZANBUDDY_WECHAT_SERIAL")
	}
	if p.PrivateKeyPEM == "" {
		missing = append(missing, "ZANBUDDY_WECHAT_KEY_PEM")
	}
	if p.APIV3Key == "" {
		missing = append(missing, "ZANBUDDY_WECHAT_APIV3_KEY")
	}
	if p.NotifyURL == "" {
		missing = append(missing, "ZANBUDDY_WECHAT_NOTIFY")
	}
	return mode, len(missing) == 0, missing
}

// Notify 微信支付结果通知(v3 回调)解析。
// live=false 时退化为 stub: 直接按 JSON {out_trade_no, trade_state} 解析(便于本地验证接线)。
// live=true 时的真实形态需: 用 APIv3 密钥(API_V3_KEY)对 resource.ciphertext 做 AES-256-GCM 解密,
// 解出 out_trade_no 与 trade_state==SUCCESS 才放行。此处保留解密骨架(需补充 APIv3Key 配置字段)。
func (p *WeChatProvider) Notify(body []byte) (string, bool) {
	if !p.live {
		var n struct {
			OutTradeNo string `json:"out_trade_no"`
			TradeState string `json:"trade_state"`
		}
		if err := json.Unmarshal(body, &n); err != nil || n.OutTradeNo == "" {
			return "", false
		}
		return n.OutTradeNo, n.TradeState == "SUCCESS" || n.TradeState == ""
	}
	// TODO(real): AES-256-GCM 解密 resource, 校验签名, 提取 out_trade_no/trade_state。
	if p.APIV3Key == "" {
		fmt.Println("[payment.wechat] 收到真实支付回调, 但 ZANBUDDY_WECHAT_APIV3_KEY 未配置, 无法解密 resource")
		return "", false
	}
	fmt.Println("[payment.wechat] 收到真实支付回调, 但 APIv3 解密骨架待接入(需补全 AES-256-GCM 解密)")
	return "", false
}
