package httpapi

import (
	"errors"
	"math"
	"net/http"
	"strings"

	"zanbuddy/internal/match"
)

// taskMatches 给定需求单, 返回最多 3 个候选人, 每人带 2-3 条带锚点的理由。
//
// 只有发单本人能看。别人调这个接口不只是"看到几个名字"——
// 推荐位反映的是这单现在有没有人抢、平台觉得它好不好接, 那是发单方的信息。
func (s *Server) taskMatches(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	taskID := r.PathValue("id")

	t, ok := s.Store.GetTask(taskID)
	if !ok {
		WriteErr(w, r, http.StatusNotFound, "TASK_NOT_FOUND", "这条需求找不到了")
		return
	}
	// 先判归属, 再判状态。顺序反了, 一个拿着别人单号来试的人就能从
	// "已经有人接了"这个错误里读到别人的进展。
	if t.RequesterID != cl.UserID {
		WriteErr(w, r, http.StatusForbidden, "NOT_TASK_OWNER", "这不是你发的需求")
		return
	}

	cands, err := s.Match.Candidates(taskID)
	if err != nil {
		writeMatchErr(w, r, err)
		return
	}

	items := make([]matchCard, 0, len(cands))
	for _, c := range cands {
		items = append(items, toCard(c))
	}
	WriteOK(w, r, map[string]interface{}{
		"task_id":    taskID,
		"candidates": items,
		"count":      len(items),
	})
}

// matchCard 一张候选人卡的对外形状。
//
// primary_endorsement 现在恒为 null(M4 才有背书数据), 但字段要留着:
// 前端不需要为"有没有背书"写两套布局, 拿到 null 就少渲染一块。
//
// portfolio 只带第一条, 不是偷懒: 档案允许存 3 条, 全搬上卡会把一张卡变成简历。
// 第一条是他自己排在最前面的, 也就是他最想让人看见的那个。
// 有它才是"能看着真东西做决定"; 只有技能标签的话, 需求方看到的全是自述。
type matchCard struct {
	MatchID    string            `json:"match_id"`
	UserID     string            `json:"user_id"`
	Slug       string            `json:"slug"`
	Name       string            `json:"name"`
	Headline   string            `json:"headline"`
	Skills     []string          `json:"skills"`
	Portfolio  []portfolioView   `json:"portfolio"`
	Score      float64           `json:"score"`
	Stats      matchStats        `json:"stats"`
	Reasons    []matchReasonView `json:"reasons"`
	Selectable bool              `json:"selectable"`
	// PrimaryEndorsement 卡上那句客户背书。没有背书就是 null，前端少渲染一块。
	// 它跟理由的区别：理由是平台算出来的，背书是别人愿意具名说的话——
	// 后者不可编辑、不可撤销，说错了有代价。
	PrimaryEndorsement *endorsementBrief `json:"primary_endorsement"`
}

// endorsementBrief 候选卡上的背书摘要。
//
// 只给摘录不给全文：全文最长 1000 字，摆进一张候选人卡会把卡撑成一封信。
// 想读全文点开信用页，那里有完整原文和所有追加。
type endorsementBrief struct {
	ID         string `json:"id"`
	AuthorName string `json:"author_name"`
	Relation   string `json:"relation"`
	Excerpt    string `json:"excerpt"`
}

// portfolioView 作品链接。label 是给人看的名字, url 是要点得开的地址。
type portfolioView struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type matchStats struct {
	Orders  int `json:"orders"`
	AvgDays int `json:"avg_days"`
	Repeat  int `json:"repeat"`
}

// matchReasonView 一条理由 + 它的锚点。前端点 ↗ 就把 anchor.label 摆出来,
// 所以没有锚点的理由在这里根本不会出现。
type matchReasonView struct {
	Text   string      `json:"text"`
	Anchor matchAnchor `json:"anchor"`
}

type matchAnchor struct {
	Type  string `json:"type"`
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

func toCard(c match.Candidate) matchCard {
	skills := c.Profile.Skills
	if len(skills) == 0 && c.User != nil {
		skills = c.User.Skills
	}
	if skills == nil {
		skills = []string{}
	}
	reasons := make([]matchReasonView, 0, len(c.Reasons))
	for _, r := range c.Reasons {
		reasons = append(reasons, matchReasonView{
			Text:   r.Text,
			Anchor: matchAnchor{Type: r.AnchorType, Ref: r.AnchorRef, Label: r.AnchorLabel},
		})
	}
	// 作品只带第一条, 且必须是能点开的地址: 存了个空 label 的脏数据不该
	// 变成卡片上一个点了没反应的「作品」。
	portfolio := make([]portfolioView, 0, 1)
	if c.Profile != nil {
		for _, p := range c.Profile.Portfolio {
			if strings.HasPrefix(p.URL, "http://") || strings.HasPrefix(p.URL, "https://") {
				portfolio = append(portfolio, portfolioView{Label: p.Label, URL: p.URL})
				break
			}
		}
	}
	card := matchCard{
		MatchID:    c.MatchID,
		UserID:     c.User.ID,
		Slug:       c.User.Slug,
		Name:       c.User.Name,
		Headline:   c.Profile.Headline,
		Skills:     skills,
		Portfolio:  portfolio,
		Score:      math.Round(c.Score*10) / 10,
		Stats:      matchStats{Orders: c.Stats.Orders, AvgDays: c.Stats.AvgDays, Repeat: c.Stats.RepeatClients},
		Reasons:    reasons,
		Selectable: true,
	}
	if c.Facts.EndorsementID != "" {
		card.PrimaryEndorsement = &endorsementBrief{
			ID:         c.Facts.EndorsementID,
			AuthorName: c.Facts.EndorsementAuthor,
			Relation:   c.Facts.EndorsementRelation,
			Excerpt:    excerptRunes(c.Facts.EndorsementQuote, 60),
		}
	}
	return card
}

// excerptRunes 按 rune 截断。按字节截会把汉字劈成半个，序列化出去是乱码。
func excerptRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func writeMatchErr(w http.ResponseWriter, r *http.Request, err error) {
	var me *match.Error
	if errors.As(err, &me) {
		WriteErrField(w, r, me.Status, me.Code, me.Msg, "", me.Hint)
		return
	}
	WriteErr(w, r, http.StatusInternalServerError, "INTERNAL", "服务出了点问题，稍后再试")
}
