// endorsement.go 背书：写、追加、读邀请详情。
//
// 为什么背书和互评分成两个文件而不是合并：
// 互评管的是"这一次合作打几分"，背书管的是"这个人值不值得下一个客户找他"。
// 前者是双向的、同时揭盲的、会消失的（未揭盲等于不存在）；
// 后者是单向的、永久公开的、不可撤销的。规则没有一个字重合，混在一起必然打架。
//
// 两套并存的这段时间（M4 切换期），review 仍然可写可读，背书是新增的一层。
// 契约 §2.10 写清了切换动作：背书跑起来、历史回填完，才停 review 的写入口。
// 现在只是把背书这条管子接上，不动 review。
package trade

import (
	"errors"
	"strings"

	"zanbuddy/internal/model"
	"zanbuddy/internal/store"
)

var (
	ErrEndorseTooShort = &Error{409, "ENDORSEMENT_TOO_SHORT",
		"背书至少 12 个字", "写清楚他做了什么：哪一版、哪一部分、你自己拿去用了吗"}
	ErrEndorseTooLong = &Error{400, "ENDORSEMENT_TOO_LONG",
		"背书太长了", "1000 字以内，说清一件事比列十条更有用"}
	ErrEndorseAnswers = &Error{409, "ENDORSEMENT_ANSWER_MISSING",
		"三个问题至少答两个", "一题都不答的背书，读的人拿不到任何可对照的信息"}
	ErrAlreadyEndorse = &Error{409, "DUPLICATE_ENDORSEMENT",
		"你已经给这一单写过背书了", "原文不能改；想补充就点「追加一条」"}
	ErrEndorseAppendLong = &Error{400, "ENDORSEMENT_APPEND_TOO_LONG",
		"追加内容太长了", "500 字以内，追加是补一句，不是写第二篇"}
	// 追加为空单独给一个错误码，不复用 ErrEndorseTooShort。
	// 复用的话用户提交空追加会被告知"背书至少 12 个字"——他明明是在追加，不是在写正文。
	ErrEndorseAppendEmpty = &Error{400, "ENDORSEMENT_APPEND_EMPTY",
		"追加内容不能为空", "写一句话再提交；不想补就关掉这个框"}
	ErrEndorseGone = &Error{404, "ENDORSEMENT_NOT_FOUND",
		"这条背书不在了", ""}
	ErrNotEndorseAuthor = &Error{403, "NOT_ENDORSEMENT_AUTHOR",
		"只有原作者能追加", "背书是写给对方的，路人不该往上加话"}
	ErrEndorseSave = &Error{500, "ENDORSEMENT_SAVE_FAILED",
		"背书没存上，请再试一次", ""}
)

// EndorsementInvite 写背书之前要给用户看的东西。
//
// 为什么不直接给一个空白 POST 接口：
// 写背书的人打开页面时不知道"该写多长、要答哪几问、是不是已经写过"。
// 这些规则本来就该由服务端说，前端自己写一遍就会和校验漂。
type EndorsementInvite struct {
	OrderID       string                      `json:"order_id"`
	TaskID        string                      `json:"task_id"`
	TaskTitle     string                      `json:"task_title"`
	TargetID      string                      `json:"target_id"`
	TargetName    string                      `json:"target_name"`
	Relation      string                      `json:"relation"`
	Questions     []model.EndorsementQuestion `json:"questions"`
	MinChars      int                         `json:"min_chars"`
	MaxChars      int                         `json:"max_chars"`
	AnswersNeeded int                         `json:"answers_needed"`
	Mine          *model.Endorsement          `json:"mine"`
	Appends       []*model.EndorsementAppend  `json:"appends"`
	CanWrite      bool                        `json:"can_write"`
	WhyNot        string                      `json:"why_not"`
}

// EndorsementInviteOf 读一单的背书邀请详情。只有这一单的双方能看。
func (s *Service) EndorsementInviteOf(orderID, uid string) (*EndorsementInvite, error) {
	o, ok := s.st.GetOrder(orderID)
	if !ok {
		return nil, ErrOrderNotFound
	}
	targetID, relation, err := endorsementParties(o, uid)
	if err != nil {
		return nil, err
	}

	inv := &EndorsementInvite{
		OrderID:       orderID,
		TaskID:        o.TaskID,
		TargetID:      targetID,
		Relation:      relation,
		Questions:     model.EndorsementQuestions,
		MinChars:      model.EndorsementMinChars,
		MaxChars:      model.EndorsementMaxChars,
		AnswersNeeded: model.EndorsementMinAnswers,
		Appends:       []*model.EndorsementAppend{},
	}
	if t, ok := s.st.GetTask(o.TaskID); ok {
		inv.TaskTitle = t.Title
	}
	if u, ok := s.st.GetUser(targetID); ok {
		inv.TargetName = u.Name
	}

	if mine, ok := s.st.GetEndorsementByOrderAuthor(orderID, uid); ok {
		inv.Mine = mine
		inv.Appends = s.st.ListEndorsementAppends(mine.ID)
	}

	// 能不能写，一句话说清"为什么不能"。
	// 只说 can_write=false 让前端自己猜原因，是这一层最常见的偷懒。
	switch {
	case o.Status != model.OrderCompleted:
		inv.WhyNot = "这一单还没验收通过。背书是给做完的活写的，不是给正在做的活写的"
	case inv.Mine != nil:
		inv.WhyNot = "你已经写过这一单了。原文不能编辑，想补充就用「追加一条」"
	default:
		inv.CanWrite = true
	}
	return inv, nil
}

// WriteEndorsement 写一条背书。
//
// 顺序：资格（是不是这单的人）→ 状态（单做完没、写过没）→ 内容（字数、三问）。
// 反过来的话，路人点进一单没做完的活，会先被告知"这单还没做完"，
// 等于从错误信息里读到了别人的交易进展。
func (s *Service) WriteEndorsement(orderID, uid, body string, answers map[string]string) (*model.Endorsement, error) {
	o, ok := s.st.GetOrder(orderID)
	if !ok {
		return nil, ErrOrderNotFound
	}
	targetID, relation, err := endorsementParties(o, uid)
	if err != nil {
		return nil, err
	}

	if o.Status != model.OrderCompleted {
		return nil, ErrOrderNotDone
	}
	if _, ok := s.st.GetEndorsementByOrderAuthor(orderID, uid); ok {
		return nil, ErrAlreadyEndorse
	}

	body = strings.TrimSpace(body)
	ch := model.CharCount(body)
	if ch < model.EndorsementMinChars {
		return nil, ErrEndorseTooShort
	}
	if ch > model.EndorsementMaxChars {
		return nil, ErrEndorseTooLong
	}
	clean := model.CleanAnswers(answers)
	if model.AnsweredCount(clean) < model.EndorsementMinAnswers {
		return nil, ErrEndorseAnswers
	}

	e := &model.Endorsement{
		ID:        s.st.NextID("EN"),
		OrderID:   orderID,
		TaskID:    o.TaskID,
		AuthorID:  uid,
		TargetID:  targetID,
		Relation:  relation,
		Body:      body,
		Answers:   clean,
		CharCount: ch,
		Status:    model.EndorseActive,
		// 权重先给满，由存储层按"12 个月内是否已有一条计权重的同向背书"降到 0。
		// 不在这一层算：这里读一次历史、存储层再写，中间有窗口。
		Weight:    100,
		Source:    model.EndorseLive,
		CreatedAt: model.Now(),
	}
	if err := s.st.PutEndorsement(e); err != nil {
		if errors.Is(err, store.ErrEndorsementExists) {
			return nil, ErrAlreadyEndorse
		}
		return nil, ErrEndorseSave
	}
	return e, nil
}

// AppendEndorsement 给一条已有背书追加内容。只有原作者能写。
//
// 为什么没有"编辑"：可编辑的背书等于可撤销的承诺。
// 被背书的人可能已经把这条背书截图发给客户了，原作者回头一改，对方手里的和页面上就不是一回事。
func (s *Service) AppendEndorsement(endorsementID, uid, body string) (*model.EndorsementAppend, error) {
	e, ok := s.st.GetEndorsement(endorsementID)
	if !ok {
		return nil, ErrEndorseGone
	}
	// void 的背书按"不在了"处理：它已经不展示给任何人，把 404 换成 403 等于告诉外人"这条存在过"。
	// flagged 不挡：flagged 的语义是"待人工复核、页面照常展示并带标记"，
	// 页面上明明摆着这条、还给了追加框，提交却回"不在了"，那是一句假话。
	if e.Status == model.EndorseVoid {
		return nil, ErrEndorseGone
	}
	if e.AuthorID != uid {
		return nil, ErrNotEndorseAuthor
	}

	body = strings.TrimSpace(body)
	if body == "" {
		return nil, ErrEndorseAppendEmpty
	}
	if model.CharCount(body) > model.EndorsementAppendMaxChars {
		return nil, ErrEndorseAppendLong
	}

	a := &model.EndorsementAppend{
		ID:            s.st.NextID("EA"),
		EndorsementID: endorsementID,
		AuthorID:      uid,
		Body:          body,
		CreatedAt:     model.Now(),
	}
	if err := s.st.PutEndorsementAppend(a); err != nil {
		return nil, ErrEndorseSave
	}
	return a, nil
}

// MyEndorsements 我收到的和我写出去的。
//
// 收到的那一列只给计权重的：零权重的是"一年内第二次合作"，
// 用户读到会以为自己有一条背书不算数，但他什么都不用做——这条就是只留痕。
// 写出去的那一列给全部，因为那是他自己做的事。
func (s *Service) MyEndorsements(uid string) (received, given []*model.Endorsement) {
	return s.st.ListEndorsementsByTarget(uid), s.st.ListEndorsementsByAuthor(uid)
}

// endorsementParties 判这一单里 uid 是甲方还是乙方，返回被背书人和关系。
//
// 自己不能给自己写：o.RequesterID == o.ProviderID 这种脏数据在库里不该存在，
// 但一旦存在，就会产生一条"自己给自己背书"的核心资产。宁可报错。
func endorsementParties(o *model.Order, uid string) (targetID, relation string, err error) {
	switch uid {
	case o.RequesterID:
		if o.ProviderID == "" || o.ProviderID == uid {
			return "", "", ErrNoParticipant
		}
		return o.ProviderID, model.RelationClient, nil
	case o.ProviderID:
		if o.ProviderID == "" {
			return "", "", ErrNoParticipant
		}
		return o.RequesterID, model.RelationPeer, nil
	default:
		return "", "", ErrNoParticipant
	}
}
