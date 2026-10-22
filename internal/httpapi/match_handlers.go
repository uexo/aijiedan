package httpapi

import (
	"errors"
	"math"
	"net/http"

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
type matchCard struct {
	MatchID    string            `json:"match_id"`
	UserID     string            `json:"user_id"`
	Slug       string            `json:"slug"`
	Name       string            `json:"name"`
	Headline   string            `json:"headline"`
	Skills     []string          `json:"skills"`
	Score      float64           `json:"score"`
	Stats      matchStats        `json:"stats"`
	Reasons    []matchReasonView `json:"reasons"`
	Selectable bool              `json:"selectable"`
	// PrimaryEndorsement 留空占位, 序列化成 null
	PrimaryEndorsement interface{} `json:"primary_endorsement"`
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
	card := matchCard{
		MatchID:    c.MatchID,
		UserID:     c.User.ID,
		Slug:       c.User.Slug,
		Name:       c.User.Name,
		Headline:   c.Profile.Headline,
		Skills:     skills,
		Score:      math.Round(c.Score*10) / 10,
		Stats:      matchStats{Orders: c.Stats.Orders, AvgDays: c.Stats.AvgDays, Repeat: c.Stats.RepeatClients},
		Reasons:    reasons,
		Selectable: true,
	}
	return card
}

func writeMatchErr(w http.ResponseWriter, r *http.Request, err error) {
	var me *match.Error
	if errors.As(err, &me) {
		WriteErrField(w, r, me.Status, me.Code, me.Msg, "", me.Hint)
		return
	}
	WriteErr(w, r, http.StatusInternalServerError, "INTERNAL", "服务出了点问题，稍后再试")
}
