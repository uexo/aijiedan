package demand

// 把一条需求发出去：一句能直接说出口的话，一个能直达认领的短链。
//
// 为什么要在服务端算这句话，而不是让前端拼字符串：
// 这句话是这一单唯一被人读到的东西——发单的人把它复制到微信里，
// 收单的人只看见这一行。文案错了，链接再对也没人点。
// 前端拼的话，大厅卡、小程序、开放 API 三个入口会各拼一个版本，早晚不一致。
//
// 为什么不是只给一个 URL：
// 发单的人把链接发给朋友，是拿自己的信用在担保。他卡住的不是"复制"这一步，
// 是不知道该怎么开口。给一句现成的话，他才发得出去。

import (
	"fmt"
	"strconv"
	"strings"

	"zanbuddy/internal/model"
)

// Invite 发出去用的物料 + 它被打开的情况。
type Invite struct {
	TaskID   string `json:"task_id"`
	URL      string `json:"url"`
	Sentence string `json:"sentence"`
	// Opens 被打开过几次。0 不代表"没人要"，只代表"还没人知道"。
	Opens int `json:"opens"`
	// FirstOpenAt 第一次被打开的时刻，0 = 还没人打开。
	FirstOpenAt int64 `json:"first_open_at"`
	// CanRedo 这一单还能不能重新发（收工或撤回的单，再发出去是自欺）。
	CanRedo bool   `json:"can_redo"`
	WhyNot  string `json:"why_not"`
}

// InviteOf 发单方取自己这一单的发送物料。不是自己的单看不到——
// 这句话里带价格和标题，只有发单的人有资格决定它被怎么说出去。
func (s *Service) InviteOf(taskID, requesterID, baseURL string) (*Invite, error) {
	t, ok := s.st.GetTask(taskID)
	if !ok {
		return nil, ErrTaskNotFound
	}
	if t.RequesterID != requesterID {
		return nil, ErrNotRequester
	}
	return s.inviteOf(t, baseURL), nil
}

func (s *Service) inviteOf(t *model.Task, baseURL string) *Invite {
	url := strings.TrimRight(baseURL, "/") + "/t/" + t.ID
	inv := &Invite{
		TaskID:   t.ID,
		URL:      url,
		Sentence: InviteSentence(t, url),
		CanRedo:  t.Status == model.TaskOpen,
	}
	if !inv.CanRedo {
		inv.WhyNot = "这一单已经" + taskClosedWord(t.Status) + "，再发链接出去，对方点开会看到一句「不在了」"
	}
	for _, h := range s.st.ListInviteHits([]string{t.ID}) {
		inv.Opens++
		if inv.FirstOpenAt == 0 || h.OpenedAt < inv.FirstOpenAt {
			inv.FirstOpenAt = h.OpenedAt
		}
	}
	return inv
}

// InviteSentence 生成那句能直接发出去的话。
//
// 结构固定为三段：我在做什么 / 多少钱 / 你要不要接 + 链接。
// 不加"急"、不加"诚心找"——那些词是求人，求人会让对方觉得这事不靠谱。
// 说清楚是什么活、给多少钱，比任何形容词都更能让人回复。
func InviteSentence(t *model.Task, url string) string {
	title := strings.TrimSpace(t.Title)
	if title == "" {
		title = "一个活"
	}
	return fmt.Sprintf("我在做一个「%s」的活，预算 %s，你要不要接：%s",
		title, yuan(t.BudgetCents), url)
}

// yuan 分 → 人话金额。整数元不带小数：3999 元比 3999.00 元更像人说的话，
// 而 39.9 元必须留小数——把 39.9 说成 39 是少给钱，不是简洁。
func yuan(cents int64) string {
	if cents%100 == 0 {
		return strconv.FormatInt(cents/100, 10) + " 元"
	}
	return strconv.FormatFloat(float64(cents)/100, 'f', 2, 64) + " 元"
}

func taskClosedWord(st model.TaskStatus) string {
	switch st {
	case model.TaskMatched:
		return "有人接了"
	case model.TaskClosed:
		return "做完收工"
	case model.TaskWithdrawn:
		return "撤回"
	default:
		return "不在开放状态"
	}
}

// RecordInviteOpen 记一次"链接被打开"。没有鉴权：链接本来就是公开的。
//
// 单子不存在就不记：那是别人拼错的链接或者爬虫，记进来会把
// 「传到了没有」这个数算脏。
func (s *Service) RecordInviteOpen(taskID string) {
	if taskID == "" {
		return
	}
	if _, ok := s.st.GetTask(taskID); !ok {
		return
	}
	s.st.PutInviteHit(&model.InviteHit{TaskID: taskID, OpenedAt: model.Now()})
}

// InviteStats 那个唯一的数：发出去的单，24 小时内有人点进来的比例。
//
// 为什么要单独算"还没到期"的那部分：刚发出去 5 分钟的单，如果直接算进分母，
// 比例会被系统性地压低，看起来像"没人感兴趣"，其实只是"还没到时间"。
// 把这两件事分开报，人就看得懂自己在等什么。
type InviteStats struct {
	WindowHours int `json:"window_hours"`
	// Published 分母：这个人发过的全部单。
	Published int `json:"published"`
	// OpenedInTime 分子：发单后 24 小时内至少被打开过一次的单。
	OpenedInTime int `json:"opened_in_time"`
	// TooEarly 还没满 24 小时、结论未定的单。它们既不在分子也不在分母里。
	TooEarly int `json:"too_early"`
	// Ratio 分子 /（分母 − 未到期）。没有可判定的单时为 0，不是 NaN。
	Ratio float64 `json:"ratio"`
}

// InviteStatsOf 算这个人的"传到了没有"。
func (s *Service) InviteStatsOf(requesterID string) *InviteStats {
	return s.inviteStatsOf(requesterID, model.Now())
}

func (s *Service) inviteStatsOf(requesterID string, now int64) *InviteStats {
	tasks := s.st.ListTasksByRequester(requesterID)
	st := &InviteStats{WindowHours: model.InviteWindowHours}

	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	firstAt := map[string]int64{}
	for _, h := range s.st.ListInviteHits(ids) {
		if cur, ok := firstAt[h.TaskID]; !ok || h.OpenedAt < cur {
			firstAt[h.TaskID] = h.OpenedAt
		}
	}

	window := int64(model.InviteWindowHours) * 60 * 60 * 1000
	decidable := 0
	for _, t := range tasks {
		st.Published++
		open, opened := firstAt[t.ID]
		// 先判"已经有人点进来了没有"，再判到期：一单发出去 10 分钟就被人点开，
		// 它已经有结论了，不该被归到"还没到时间"里。
		if opened && open >= t.CreatedAt && open-t.CreatedAt <= window {
			st.OpenedInTime++
			decidable++
			continue
		}
		if now-t.CreatedAt < window {
			st.TooEarly++
			continue
		}
		decidable++
	}
	if decidable > 0 {
		st.Ratio = float64(st.OpenedInTime) / float64(decidable)
	}
	return st
}
