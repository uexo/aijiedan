package match

import "strings"

// 五因子权重。数值与 v1 完全一致，不在这轮调整：
// 权重是产品决策，改了它等于改了"什么样的人算合适"，那要单独一轮评估，
// 不能顺手塞进一个"补齐功能"的提交里。
const (
	WeightSkillHit      = 12.0 // 每个命中技能
	WeightSkillCap      = 30.0 // 技能项封顶
	WeightSemantic      = 25.0
	WeightCredit        = 20.0
	WeightTaxBound      = 5.0
	WeightHistory       = 20.0
	RecallSemanticFloor = 0.1 // 语义召回门槛：无字面命中时，余弦低于它不进候选
)

// Profile 打分需要的接单方事实。刻意只放事实，不放 model.User：
// 这个包要能在没有任何 IO 的单测里跑，输入换掉就能复现同一分数。
type Profile struct {
	Skills      []string
	CreditScore int
	TaxBound    bool
	AvgRating   float64 // 历史互评均值 0-5，0 表示还没有评价
}

// Score 五因子明细 + 总分。
// 明细不是给前端看的装饰：理由生成要按因子分排序，审计要能复算每一项。
type Score struct {
	Skill    float64 // 技能字面命中
	Semantic float64 // 语义余弦折算
	Credit   float64 // 信用分折算
	Tax      float64 // 税源绑定加成
	History  float64 // 历史评分折算
	Total    float64

	Hits      []string // 命中的技能标签(原始大小写, 直接进理由文案)
	Sem       float64  // 原始余弦值, 用于文案与门槛判断
	AvgRating float64
}

// Recalled 是否过召回门槛。
//
// 门槛的含义：能说清"为什么是他"才有资格进候选。技能有交集，或者语义足够接近，
// 两条至少占一条。两条都不占的人进了候选，卡片上只能写"综合匹配度高"——那是废话，
// 而且是编的。
func (s Score) Recalled() bool {
	return len(s.Hits) > 0 || s.Sem >= RecallSemanticFloor
}

// Compute 按五因子算分。sem 由调用方用 Embedding 算好传进来，
// 因为向量化是批量做的（一次请求内所有候选共用一次 embed 调用），
// 放在这里逐人算会把批量优势丢掉。
func Compute(taskDoc string, p Profile, sem float64) Score {
	hits := skillHits(taskDoc, p.Skills)

	skillScore := float64(len(hits)) * WeightSkillHit
	if skillScore > WeightSkillCap {
		skillScore = WeightSkillCap
	}
	semScore := sem * WeightSemantic
	creditScore := float64(p.CreditScore) / 100 * WeightCredit
	taxScore := 0.0
	if p.TaxBound {
		taxScore = WeightTaxBound
	}
	ratingScore := p.AvgRating / 5 * WeightHistory

	total := skillScore + semScore + creditScore + taxScore + ratingScore
	if total > 100 {
		total = 100
	}
	return Score{
		Skill:     skillScore,
		Semantic:  semScore,
		Credit:    creditScore,
		Tax:       taxScore,
		History:   ratingScore,
		Total:     total,
		Hits:      hits,
		Sem:       sem,
		AvgRating: p.AvgRating,
	}
}

// skillHits 找出需求文本里命中的技能标签。
//
// 与 v1 的差别只在英文：v1 用 strings.Contains，于是技能 "go" 会命中 "django"、
// "logo"、"google"，简历上写 Go 的人被推给所有提到 logo 的需求，理由写着
// "技能命中 go"——需求方一点开抽屉就发现是假锚点。英文按词比对，"go" 只命中
// 独立的 go；中文没有词边界，仍然用包含（"数据抓取" 命中 "数据抓取与清洗" 是对的）。
func skillHits(taskDoc string, skills []string) []string {
	if len(skills) == 0 {
		return nil
	}
	lower := strings.ToLower(taskDoc)
	words := map[string]bool{}
	for _, tok := range Tokenize(lower) {
		words[tok] = true
	}
	var hits []string
	for _, raw := range skills {
		k := strings.ToLower(strings.TrimSpace(raw))
		if k == "" {
			continue
		}
		if isASCIIWord(k) {
			if words[k] {
				hits = append(hits, raw)
			}
			continue
		}
		if strings.Contains(lower, k) {
			hits = append(hits, raw)
		}
	}
	return hits
}

// isASCIIWord 判断是否整串都是 ASCII。
// 只有 ASCII 能用"按词精确比对"：Tokenize 把英文按词切、中文按字切，
// 中文标签拿去查 words 永远查不到（它被拆成单字了）。
func isASCIIWord(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
