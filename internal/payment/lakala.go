// 拉卡拉分账通 Provider —— 合规线 C3 选定的生产资金通道(持牌机构分账/空中分账)。
//
// 为什么必须存在这个 Provider(红线背景):
//   平台现行「付款→平台托管→验收放款+抽佣」若由平台自有账户经手资金, 监管定性接近「二清」
//   (国务院令768号《非银行支付机构监督管理条例》+ 银发办〔2017〕217号), 后果含实控人刑责。
//   合规解 = 资金全程留在持牌机构侧, 平台只下发分账指令, 这就是分账通模式:
//     需求方付款 → 资金进拉卡拉监管账户(平台自有账户不过钱) → 验收后按分账方案
//     「接单方 N% + 平台佣金 10%」由持牌机构直接清算到各方入账账户。
//   对比: 微信/支付宝原生分账仅能分出订单额的 30%, 余款滞留平台商户号 = 仍违规, 不可用。
//
// 行为约定(与 WeChatProvider 的关键差异):
//   - fail-closed: 选了 lakala 但凭证不全时, Escrow/Release/Refund 一律返回 error,
//     绝不静默退回 stub 形态——本通道是合规生命线, 静默降级 = 假装有资金监管, 比报错危险得多。
//   - live=true 时才发起真实 HTTPS 请求; 具体字段以与拉卡拉签约后的接口文档为准,
//     此处为合同形态骨架(端点/请求响应结构/签名方式已按开放平台通行 v3 风格实现)。
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

const (
	lklDefaultBase = "https://openapi.lakala.com"          // 拉卡拉开放平台(生产以签约文档为准)
	lklEscrowPath  = "/api/v3/lzorder/escrow"              // 托管下单(资金进监管账户)
	lklSplitPath   = "/api/v3/split/orders"                // 分账(多方清算, 平台不沉淀)
	lklRefundPath  = "/api/v3/refund/orders"               // 退款(原路退回)
)

// LakalaProvider 拉卡拉分账通真实形态骨架。
type LakalaProvider struct {
	MchID         string // 平台主商户号(分账通进件)
	AppID         string // 开放平台应用 AppID
	SerialNo      string // 商户证书序列号
	PrivateKeyPEM string // RSA 私钥(签名)
	NotifyURL     string // 异步通知地址
	BaseURL       string // 可调, 便于联调环境
	live          bool
	key           *rsa.PrivateKey
}

// NewLakalaProvider 从环境变量构造; 四项核心凭证齐全才 live=true。
func NewLakalaProvider() *LakalaProvider {
	p := &LakalaProvider{
		MchID:         os.Getenv("ZANBUDDY_LAKALA_MCHID"),
		AppID:         os.Getenv("ZANBUDDY_LAKALA_APPID"),
		SerialNo:      os.Getenv("ZANBUDDY_LAKALA_SERIAL"),
		PrivateKeyPEM: os.Getenv("ZANBUDDY_LAKALA_KEY_PEM"),
		NotifyURL:     os.Getenv("ZANBUDDY_LAKALA_NOTIFY"),
		BaseURL:       os.Getenv("ZANBUDDY_LAKALA_API"),
	}
	if p.BaseURL == "" {
		p.BaseURL = lklDefaultBase
	}
	p.live = p.MchID != "" && p.AppID != "" && p.SerialNo != "" && p.PrivateKeyPEM != ""
	if !p.live {
		fmt.Println("[payment.lakala] 分账通凭证不全, 通道 fail-closed: 托管/分账/退款将报错直至凭证配齐")
	}
	return p
}

// missing 返回未配置的核心凭证清单(供 Readiness 上报)。
func (p *LakalaProvider) missing() []string {
	var m []string
	if p.MchID == "" {
		m = append(m, "ZANBUDDY_LAKALA_MCHID")
	}
	if p.AppID == "" {
		m = append(m, "ZANBUDDY_LAKALA_APPID")
	}
	if p.SerialNo == "" {
		m = append(m, "ZANBUDDY_LAKALA_SERIAL")
	}
	if p.PrivateKeyPEM == "" {
		m = append(m, "ZANBUDDY_LAKALA_KEY_PEM")
	}
	if p.NotifyURL == "" {
		m = append(m, "ZANBUDDY_LAKALA_NOTIFY")
	}
	return m
}

func (p *LakalaProvider) privateKey() (*rsa.PrivateKey, error) {
	if p.key != nil {
		return p.key, nil
	}
	block, _ := pem.Decode([]byte(p.PrivateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("拉卡拉私钥 PEM 解析失败")
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
	return nil, fmt.Errorf("拉卡拉私钥解析失败(非 RSA 私钥)")
}

// genAuth 生成 v3 风格 Authorization 头(RSA-SHA256 签名, 与微信支付同构)。
func (p *LakalaProvider) genAuth(method, path, body string, ts int64, nonce string) (string, error) {
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
	return fmt.Sprintf(`LAKALA-SHA256-RSA2048 mchid="%s",appid="%s",nonce_str="%s",signature="%s",timestamp="%d",serial_no="%s"`,
		p.MchID, p.AppID, nonce, base64.StdEncoding.EncodeToString(sig), ts, p.SerialNo), nil
}

func (p *LakalaProvider) doPost(path, body string) ([]byte, error) {
	ts := time.Now().Unix()
	nonce := genNonce()
	auth, err := p.genAuth("POST", path, body, ts, nonce)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", p.BaseURL+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
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
		return buf, fmt.Errorf("拉卡拉 HTTP %d: %s", resp.StatusCode, string(buf))
	}
	return buf, nil
}

// ---- 请求/响应结构体(合同形态, 以签约后接口文档为准) ----

type lklEscrowReq struct {
	MchID      string `json:"mch_id"`
	AppID      string `json:"app_id"`
	OutOrderNo string `json:"out_order_no"`
	NotifyURL  string `json:"notify_url"`
	Amount     int64  `json:"amount"` // 分
	Currency   string `json:"currency"`
	PayerID    string `json:"payer_id"` // 需求方在分账通的付款主体标识
	Scene      string `json:"scene"`    // ESCROW=托管交易
}
type lklEscrowRsp struct {
	OutOrderNo string `json:"out_order_no"`
	TradeNo    string `json:"trade_no"` // 拉卡拉侧交易号(作托管单号)
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type lklReceiver struct {
	Account     string `json:"account"` // 接单方/平台的入账主体号(分账通进件)
	Amount      int64  `json:"amount"`  // 分
	Description string `json:"description"`
}
type lklSplitReq struct {
	MchID      string        `json:"mch_id"`
	AppID      string        `json:"app_id"`
	OutOrderNo string        `json:"out_order_no"` // 原托管单号
	OutSplitNo string        `json:"out_split_no"`
	Receivers  []lklReceiver `json:"receivers"`
}
type lklSplitRsp struct {
	OutSplitNo string `json:"out_split_no"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type lklRefundReq struct {
	MchID       string `json:"mch_id"`
	AppID       string `json:"app_id"`
	OutOrderNo  string `json:"out_order_no"`
	OutRefundNo string `json:"out_refund_no"`
	Amount      int64  `json:"amount"` // 分
	Reason      string `json:"reason"`
}
type lklRefundRsp struct {
	OutRefundNo string `json:"out_refund_no"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}

// Escrow 托管下单: 需求方资金进拉卡拉监管账户, 平台自有账户不过钱。返回拉卡拉交易号。
func (p *LakalaProvider) Escrow(orderID string, amount int64) (string, error) {
	if !p.live {
		return "", fmt.Errorf("拉卡拉分账通未就绪(缺凭证: %v), 按合规要求拒绝托管(fail-closed)", p.missing())
	}
	req := lklEscrowReq{
		MchID:      p.MchID,
		AppID:      p.AppID,
		OutOrderNo: orderID,
		NotifyURL:  p.NotifyURL,
		Amount:     amount,
		Currency:   "CNY",
		Scene:      "ESCROW",
	}
	b, _ := json.Marshal(req)
	data, err := p.doPost(lklEscrowPath, string(b))
	if err != nil {
		return "", err
	}
	var r lklEscrowRsp
	if e := json.Unmarshal(data, &r); e != nil {
		return "", fmt.Errorf("拉卡拉托管响应解析失败: %w", e)
	}
	if r.TradeNo == "" {
		return "", fmt.Errorf("拉卡拉托管下单失败: %s %s", r.Code, r.Message)
	}
	return r.TradeNo, nil
}

// Release 验收后分账: 从监管账户按方案清算给接单方 + 平台佣金, 资金不进平台自有账户。
func (p *LakalaProvider) Release(escrowID string, toProvider, toPlatform int64) error {
	if !p.live {
		return fmt.Errorf("拉卡拉分账通未就绪(缺凭证: %v), 按合规要求拒绝分账放款(fail-closed)", p.missing())
	}
	req := lklSplitReq{
		MchID:      p.MchID,
		AppID:      p.AppID,
		OutOrderNo: escrowID,
		OutSplitNo: "SP_" + escrowID,
		Receivers: []lklReceiver{
			{Account: "${provider_account}", Amount: toProvider, Description: "接单方结算"},
			{Account: p.MchID, Amount: toPlatform, Description: "平台佣金"},
		},
	}
	b, _ := json.Marshal(req)
	data, err := p.doPost(lklSplitPath, string(b))
	if err != nil {
		return err
	}
	var r lklSplitRsp
	if e := json.Unmarshal(data, &r); e != nil {
		return fmt.Errorf("拉卡拉分账响应解析失败: %w", e)
	}
	if r.OutSplitNo == "" {
		return fmt.Errorf("拉卡拉分账失败: %s %s", r.Code, r.Message)
	}
	return nil
}

// Refund 退款: 从监管账户原路退回需求方。
func (p *LakalaProvider) Refund(escrowID string, toRequester int64) error {
	if !p.live {
		return fmt.Errorf("拉卡拉分账通未就绪(缺凭证: %v), 按合规要求拒绝退款(fail-closed)", p.missing())
	}
	req := lklRefundReq{
		MchID:       p.MchID,
		AppID:       p.AppID,
		OutOrderNo:  escrowID,
		OutRefundNo: "RF_" + escrowID,
		Amount:      toRequester,
		Reason:      "订单退款/纠纷",
	}
	b, _ := json.Marshal(req)
	data, err := p.doPost(lklRefundPath, string(b))
	if err != nil {
		return err
	}
	var r lklRefundRsp
	if e := json.Unmarshal(data, &r); e != nil {
		return fmt.Errorf("拉卡拉退款响应解析失败: %w", e)
	}
	if r.OutRefundNo == "" {
		return fmt.Errorf("拉卡拉退款失败: %s %s", r.Code, r.Message)
	}
	return nil
}

func (p *LakalaProvider) isLive() bool { return p.live }

// Notify 拉卡拉异步通知解析(合同形态: {out_order_no, trade_status})。
// TODO(real): 按签约文档补验签/解密; 骨架阶段解析明文 JSON 便于本地验证接线。
func (p *LakalaProvider) Notify(body []byte) (string, bool) {
	var n struct {
		OutOrderNo string `json:"out_order_no"`
		TradeState string `json:"trade_status"`
	}
	if err := json.Unmarshal(body, &n); err != nil || n.OutOrderNo == "" {
		return "", false
	}
	return n.OutOrderNo, n.TradeState == "SUCCESS" || n.TradeState == ""
}
