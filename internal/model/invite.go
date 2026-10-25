package model

// InviteHit 一次"邀请链接被打开"的记录。
//
// 只有三个字段，是有意为之：现在要回答的问题只有一个——
// 「发出去 24 小时内，有没有人点进来」。要答这个问题只需要任务号和一个时刻。
// 记 IP、记 UA、记来源渠道都能"更有用"，但每一项都是要还的隐私债，
// 而现在连这一单能不能成立都还不知道，先欠着不合适。
type InviteHit struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	OpenedAt int64  `json:"opened_at"`
}

// InviteWindow 算"传到了没有"的时间窗：24 小时。
//
// 为什么是 24 小时而不是一周：这个数要的是"发出去之后立刻有人看"，
// 拉长到一周，一次转发链接的偶然点击也会被算成传到了，指标就失去分辨力。
// 24 小时没动静，按"没传出去"处理，然后去看是话没说清还是人不对。
const InviteWindowHours = 24
