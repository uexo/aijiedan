// Package match 的 M2 升级: 把语义召回层从「固定词袋余弦」抽象为「可插拔 Embedder」。
//
// M1 直接用 CosineSim(词袋TF-L2) 衡量语义。M2 抽象出 Embedder 接口, 提供两种实现:
//   - BagOfWordsEmbedder: 复用 M1 词袋向量(零依赖, 默认保底), 行为与 M1 完全一致;
//   - LLMEmbedder: 调用 OpenAI 兼容 /embeddings API(可选, env 配置), 产出稠密语义向量,
//     显著提升中文需求的语义召回精度(尤其是词袋难以覆盖的近义/上下位关系)。
//
// 调用方(Service)在召回入口 batch embed 全部文档, 再 per-pair 做向量余弦。
// LLM 未配置或调用失败时, Service 自动回退 BagOfWordsEmbedder, 保证召回不挂——
// 与项目「Stub 默认 + env 切换真实 Provider」范式一致。
package match

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"lianvo/internal/aihttp"
)

// Embedding 统一向量抽象: 内部实现可稀疏(词袋)或稠密(LLM), 自带余弦计算,
// 使召回层无需关心底层是词袋还是真实向量, 平滑切换。
type Embedding struct {
	dense  []float32          // LLM 稠密向量
	sparse map[string]float64 // 词袋稀疏向量(L2 已归一化)
}

// Cosine 两向量余弦(返回值区间 [0,1])。稀疏对稀疏 / 稠密对稠密; 跨类型(不会发生)返回 0。
func (e Embedding) Cosine(o Embedding) float64 {
	if e.dense != nil && o.dense != nil {
		return denseCosine(e.dense, o.dense)
	}
	if e.sparse != nil && o.sparse != nil {
		return sparseCosine(e.sparse, o.sparse)
	}
	return 0
}

// Mode 返回底层实现标识, 用于 readiness/日志(如 "bag-of-words" / "llm")。
func (e Embedding) Mode() string {
	if e.dense != nil {
		return "llm"
	}
	return "bag-of-words"
}

func denseCosine(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	c := dot / (math.Sqrt(na) * math.Sqrt(nb))
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// sparseCosine a、b 均已 L2 归一化(来自 Vectorize), 点积即余弦。
func sparseCosine(a, b map[string]float64) float64 {
	// 遍历较短字典以省时
	if len(a) > len(b) {
		a, b = b, a
	}
	var dot float64
	for k, ca := range a {
		if cb, ok := b[k]; ok {
			dot += ca * cb
		}
	}
	if dot < 0 {
		return 0
	}
	if dot > 1 {
		return 1
	}
	return dot
}

// Embedder 文本→向量抽象, 可插拔: 词袋(零依赖保底) / LLM 向量 API(可选)。
type Embedder interface {
	// Embed 批量将文本转为向量, 第 i 个文本对应返回第 i 个 Embedding。
	Embed(texts []string) ([]Embedding, error)
	// Mode 返回实现标识。
	Mode() string
}

// BagOfWordsEmbedder 复用 M1 词袋 TF-L2 作为 embedding(零依赖, 默认保底)。
// 与 M1 CosineSim 在数学上完全等价, 但统一到 Embedder 接口, 使召回层可平滑切换 LLM。
type BagOfWordsEmbedder struct{}

// Mode 实现 Embedder。
func (BagOfWordsEmbedder) Mode() string { return "bag-of-words" }

// Embed 实现 Embedder: 每个文本经 Vectorize(L2 归一化) 得到稀疏向量。
func (BagOfWordsEmbedder) Embed(texts []string) ([]Embedding, error) {
	out := make([]Embedding, len(texts))
	for i, t := range texts {
		out[i] = Embedding{sparse: Vectorize(t)}
	}
	return out, nil
}

// LLMEmbedder OpenAI 兼容 /embeddings API(可选)。
// 未配置或调用失败时由调用方(Service)回退 BagOfWordsEmbedder, 保证召回不挂。
// 支持 OpenAI 官方 / 本地 Ollama(v1 兼容) / 自建向量服务等任意 OpenAI 兼容端点。
type LLMEmbedder struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

// Mode 实现 Embedder。
func (*LLMEmbedder) Mode() string { return "llm" }

type embedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embed 实现 Embedder: 调用 {BaseURL}/embeddings, 批量取回稠密向量。
func (e *LLMEmbedder) Embed(texts []string) ([]Embedding, error) {
	if e.Client == nil {
		e.Client = aihttp.New(15 * time.Second)
	}
	body, err := json.Marshal(embedReq{Model: e.Model, Input: texts})
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(e.BaseURL, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed api http %d", resp.StatusCode)
	}
	var er embedResp
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	if er.Error != nil {
		return nil, fmt.Errorf("embed api error: %s", er.Error.Message)
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("embed api returned %d vectors for %d texts", len(er.Data), len(texts))
	}
	out := make([]Embedding, len(texts))
	for i, d := range er.Data {
		out[i] = Embedding{dense: d.Embedding}
	}
	return out, nil
}

// NewEmbedder 按环境变量选择 Embedder:
//   - LIANVO_EMBEDDING_MODE=llm 且配了 LIANVO_EMBEDDING_API 才走 LLM;
//   - 否则(默认)返回 BagOfWordsEmbedder, 零依赖保底, 行为与 M1 完全一致。
func NewEmbedder() Embedder {
	if strings.EqualFold(os.Getenv("LIANVO_EMBEDDING_MODE"), "llm") {
		if api := os.Getenv("LIANVO_EMBEDDING_API"); api != "" {
			return &LLMEmbedder{
				BaseURL: api,
				APIKey:  os.Getenv("LIANVO_EMBEDDING_KEY"),
				Model:   os.Getenv("LIANVO_EMBEDDING_MODEL"),
			}
		}
		// 声明了 llm 却没配 API → 回退保底, 避免静默空配置
	}
	return BagOfWordsEmbedder{}
}
