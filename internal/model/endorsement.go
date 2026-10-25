// endorsement.go 背书：产品的核心信用资产（契约 §2.2 / §2.3，PRD FR-07、FR-09）。
//
// 为什么要有这个东西，而不继续用 review 的星级：
// 星级只回答"几分"，回答不了"他到底做了什么"。12 字背书 + 三问，问的是
// 「交付对得上吗 / 哪里超出预期 / 下次还找他吗」——这三问答完，读的人能自己判断，
// 而不是替他把判断压缩成一个数字。星级是平台在做判断，背书是把判断权还给读的人。
//
// 三条不可动的约束（都写在契约里，改这里要同时改契约）：
//  1. 只有 completed 的订单能写，一笔订单一个人只能写一条（uk_order_author）。
//  2. 原文不可编辑，只能追加（endorsement_append，接口层没有 PUT）。
//  3. 同一对用户 12 个月内只有第一条计权重，后面的记录留着但 weight=0。
//
// 第 3 条为什么要留着记录而不是拒收：可信记录应该完整。一年后又合作一次，
// 那件事真实发生过，凭什么不记？但权重只算一次，否则两个人互相背书十次就成了"长期合作伙伴"。
package model

import "strings"

// 背书正文的下限。
//
// 12 这个数不是拍脑袋：一句"很好，谢谢"是 5 个字，能被任何刷单账号批量生产；
// 12 个字刚好逼出"做了什么"级别的具体信息，又不至于让人写一段作文。
const EndorsementMinChars = 12

// 背书正文上限。门槛是下限不是上限，这个数只防滥用。
const EndorsementMaxChars = 1000

// 追加内容上限。追加是补充说明，不是第二篇正文。
const EndorsementAppendMaxChars = 500

// 同一对用户重复背书的权重窗口：12 个月。
const EndorsementWeightWindowDays = 365

// 背书关系。同一单的两个方向不是同一种东西，权重与话术都该分开。
const (
	// RelationClient 需求方写给接单方：客户给他的评价。这是匹配推荐唯一该用的那种。
	RelationClient = "client"
	// RelationPeer 接单方写给需求方：同行对他的评价。用于需求方的信用，不进接单推荐。
	RelationPeer = "peer"
)

// 背书状态。void 只给运营人工复核后用，作者和平台都不能单方面删。
const (
	EndorseActive  = "active"
	EndorseFlagged = "flagged"
	EndorseVoid    = "void"
)

// 来源。legacy = 由旧 review 转换来的历史背书，权重降半。
const (
	EndorseLive   = "live"
	EndorseLegacy = "legacy"
)

// Endorsement 一条背书。
//
// Answers 存 JSON：{"delivered":"..","exceeded":"..","again":".."}。
// 三问是固定题，所以不建三张表也不建三列——列一多，加一个问题就要改表。
type Endorsement struct {
	ID        string            `json:"id"`
	OrderID   string            `json:"order_id"`
	TaskID    string            `json:"task_id"`
	AuthorID  string            `json:"author_id"` // 写背书的人
	TargetID  string            `json:"target_id"` // 被背书的人
	Relation  string            `json:"relation"`  // client / peer
	Body      string            `json:"body"`
	Answers   map[string]string `json:"answers"`
	CharCount int               `json:"char_count"` // 入库时算，门槛校验用它而不是再数一遍
	Status    string            `json:"status"`
	Weight    int               `json:"weight"` // 0-100；0 表示计入记录但不计权重
	Source    string            `json:"source"` // live / legacy
	CreatedAt int64             `json:"created_at"`
}

// EndorsementAppend 一条追加。原文不可编辑，想补充只能另起一条。
type EndorsementAppend struct {
	ID            string `json:"id"`
	EndorsementID string `json:"endorsement_id"`
	AuthorID      string `json:"author_id"`
	Body          string `json:"body"`
	CreatedAt     int64  `json:"created_at"`
}

// EndorsementQuestion 三问之一。文案放服务端：同一套问题以后要用在短信、导出、小程序上，
// 散在前端就会各写一份，改一处漏三处。
type EndorsementQuestion struct {
	Key  string `json:"key"`
	Ask  string `json:"ask"`
	Hint string `json:"hint"`
}

// EndorsementQuestions 三问。顺序即展示顺序。
//
// 为什么第二问允许写"没有"：逼人夸的结果是满屏"非常专业、效率很高"。
// 明说可以不夸，才有人敢说真话。
var EndorsementQuestions = []EndorsementQuestion{
	{Key: "delivered", Ask: "他交付的东西，和你要的对得上吗？", Hint: "说具体的东西：哪一版、哪一部分"},
	{Key: "exceeded", Ask: "哪一点超出了你的预期？", Hint: "没有就写没有，别硬夸"},
	{Key: "again", Ask: "下次有类似的活，你还会找他吗？", Hint: "会 / 不会，再说一句为什么"},
}

// EndorsementMinAnswers 至少答这么多个问题才收。
//
// 3 问里答 2 个：给"有一问答不上来"留位置，但不允许只答一题就交。
// 只答一题的背书，读的人拿不到任何交叉验证。
const EndorsementMinAnswers = 2

// CharCount 数正文字符数：去掉所有空白后的 rune 数。
//
// 为什么去空白：中文写作习惯在标点后加空格，不去掉的话
// "很好，        谢谢" 这种一眼假的填充也能过 12 字门槛。
func CharCount(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\u3000' {
			continue
		}
		n++
	}
	return n
}

// AnsweredCount 三问里实际答了几问（去空白后非空才算答）。
//
// 只认 EndorsementQuestions 里的 key：多塞的 key 不算数，
// 否则前端传四个 key 就能绕过"至少两问"。
func AnsweredCount(answers map[string]string) int {
	n := 0
	for _, q := range EndorsementQuestions {
		if strings.TrimSpace(answers[q.Key]) != "" {
			n++
		}
	}
	return n
}

// CleanAnswers 只保留三问之内的非空答案，顺便去掉首尾空白。
//
// 入库前一定要过这一道：answers 是 JSON 列，塞进去什么就存什么，
// 以后换问题文案、加第四问时，脏 key 会让"这一条答全了没有"永远算不准。
func CleanAnswers(answers map[string]string) map[string]string {
	out := make(map[string]string, len(EndorsementQuestions))
	for _, q := range EndorsementQuestions {
		if v := strings.TrimSpace(answers[q.Key]); v != "" {
			out[q.Key] = v
		}
	}
	return out
}

// EndorsementVisible 一条背书现在是否该展示给外人。
//
// flagged 不隐藏：被反刷规则标记过、还在等人工复核，藏起来等于替用户做了判断；
// 但前端要带标记说明"这条被系统标记过"。void 才是彻底不展示。
func EndorsementVisible(e *Endorsement) bool {
	if e == nil {
		return false
	}
	return e.Status == EndorseActive || e.Status == EndorseFlagged
}

// Weighted 这条背书是否计入权重。
func Weighted(e *Endorsement) bool {
	return EndorsementVisible(e) && e.Weight > 0
}
