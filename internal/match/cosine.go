// Package match 提供 M0 轻量语义召回能力。
// 采用「词袋(TF) + L2 归一化 + 余弦相似度」衡量两段文本的语义接近度，
// 纯标准库实现，零外部依赖（不接 embedding 外部服务，符合 M0 零依赖原则）。
// 用途：在规则匹配（技能字面命中）之外，补充「软匹配」召回层——
// 即使技能标签未精确字面命中，只要需求描述与接单方技能画像语义相近也可召回。
package match

import (
	"regexp"
	"strings"
)

// tokenRe 匹配连续的英文/数字串，或单个汉字，作为最小语义单元。
// 中文按字切分（中文无天然词边界），英文/数字按词切分（如 "python"、"nlp"、"1080x1440"）。
var tokenRe = regexp.MustCompile(`[a-z0-9]+|[一-鿿]`)

// Tokenize 将文本切分为小写 token 序列。
func Tokenize(text string) []string {
	lower := strings.ToLower(text)
	return tokenRe.FindAllString(lower, -1)
}

// Vectorize 将文本转为 L2 归一化的词频向量（TF）。
// 归一化后两向量的点积即余弦相似度。
func Vectorize(text string) map[string]float64 {
	v := map[string]float64{}
	for _, tok := range Tokenize(text) {
		// 过滤单字噪声（长度1且为英文/数字的无意义 token 仍保留，单汉字保留以保语义）
		v[tok]++
	}
	var norm float64
	for _, c := range v {
		norm += c * c
	}
	if norm == 0 {
		return v
	}
	n := sqrt(norm)
	for k, c := range v {
		v[k] = c / n
	}
	return v
}

// CosineSim 计算两段文本的余弦相似度，返回值区间 [0,1]。
// 两段文本均经 L2 归一化，点积即 cosine。
func CosineSim(a, b string) float64 {
	va := Vectorize(a)
	vb := Vectorize(b)
	var dot float64
	for k, ca := range va {
		if cb, ok := vb[k]; ok {
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

// sqrt 牛顿法开方（避免引入 math 依赖，保持零依赖心智；误差可忽略）。
func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 24; i++ {
		z = (z + x/z) / 2
	}
	return z
}
