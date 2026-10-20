package trade

import "zanbuddy/internal/model"

// StateOf 内部订单状态 → 对外四态。
//
// 内部状态是给资金链路用的(待托管、待结算这些中间态不能省),
// 四态是给人和页面用的。两个口径分开, 才不会为了"页面上好看"去动状态机。
func StateOf(s model.OrderStatus) model.LightState {
	switch s {
	case model.OrderActive, model.OrderDelivering:
		return model.LightInProgress
	case model.OrderDelivered:
		return model.LightToReview
	case model.OrderCompleted, model.OrderPendingSettlement:
		return model.LightDone
	case model.OrderCancelled, model.OrderDisputed:
		return model.LightCancelled
	default:
		// pending_escrow 落在待确认: 托管模式下是"还没托管",
		// 轻交易模式下是"认领了, 等需求方点头"。对用户都是"这事还没定"。
		return model.LightToConfirm
	}
}

// StateLabel 四态的中文说法。放服务端而不是前端:
// 同一套话术以后要用在短信、通知、导出的 PDF 上, 散在前端就会各写一份。
func StateLabel(s model.LightState) string {
	switch s {
	case model.LightInProgress:
		return "进行中"
	case model.LightToReview:
		return "待验收"
	case model.LightDone:
		return "已完成"
	case model.LightCancelled:
		return "已取消"
	default:
		return "待确认"
	}
}
