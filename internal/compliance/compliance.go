// Package compliance 实现Zanbuddy的合规校验与拦截逻辑(核心闸门)。
// 设计要点: 在发单/接单/托管/结算等关键节点, 校验平台级合规开关 + 用户级合规状态,
// 不满足则拦截交易并明确指出缺失项与对应的合规执行卡编号, 引导合规线(董事长侧)落地。
package compliance

import "zanbuddy/internal/model"

// Action 受合规约束的业务动作
type Action string

const (
	ActPublishTask  Action = "publish_task"  // 需求方发单
	ActAcceptTask   Action = "accept_task"   // 接单方应征/接单
	ActCreateEscrow Action = "create_escrow" // 创建资金托管(进入待支付)
	ActSettle       Action = "settle"        // 结算放款
)

// required 每个动作所需的「平台级」合规项。KYC 同时是用户级, 在 Check 内额外判断。
var required = map[Action][]model.ComplianceItem{
	ActPublishTask:  {model.ItemKYC},
	ActAcceptTask:   {model.ItemKYC, model.ItemTaxSource},
	ActCreateEscrow: {model.ItemKYC, model.ItemTaxSource, model.ItemEscrow},
	ActSettle:       {model.ItemKYC, model.ItemTaxSource, model.ItemICP, model.ItemEscrow},
}

// Block 拦截详情
type Block struct {
	Reason  string                  // 拦截原因
	Missing []model.ComplianceItem  // 缺失的合规项
	CardRef string                  // 对应的合规执行卡(指引董事长侧落地)
}

// Service 合规闸门服务。
// 平台级合规状态通过 load/save 回调读写: 内存模式读内存 map, MySQL 模式读/写 compliance_state 表。
// 用户级 KYC 默认读 u.KYCVerified; 若注入了 kycProvider 钩子(由真实/模拟 KYC 通道提供), 则以其为准,
// 实现「KYC 从纯状态位改为可查询 Provider 状态」。
type Service struct {
	load       func() *model.ComplianceState
	save       func(model.ComplianceItem, bool)
	kycProvider func(uid string) bool
}

// New 接收 load(读取平台级合规状态)与 save(持久化单项翻转)回调。
func New(load func() *model.ComplianceState, save func(model.ComplianceItem, bool)) *Service {
	return &Service{load: load, save: save}
}

// Check 校验动作是否放行。返回 (allowed, block)。
func (s *Service) Check(action Action, u *model.User) (bool, *Block) {
	state := s.load()
	var missing []model.ComplianceItem
	for _, it := range required[action] {
		if !state.Items[it] {
			missing = append(missing, it)
		}
	}
	// 用户级合规: 实名 + 接单方必须绑定税源地(个体入驻)
	if u != nil {
		kycOK := u.KYCVerified
		if s.kycProvider != nil {
			kycOK = s.kycProvider(u.ID)
		}
		if !kycOK {
			missing = appendUnique(missing, model.ItemKYC)
		}
		if (action == ActAcceptTask || action == ActSettle) && u.Role == model.RoleProvider && !u.TaxBound {
			missing = appendUnique(missing, model.ItemTaxSource)
		}
	}
	if len(missing) == 0 {
		return true, nil
	}
	return false, &Block{
		Reason:  "合规拦截: 缺少交易前置条件, 该操作被拦截",
		Missing: missing,
		CardRef: cardFor(missing),
	}
}

func appendUnique(s []model.ComplianceItem, it model.ComplianceItem) []model.ComplianceItem {
	for _, x := range s {
		if x == it {
			return s
		}
	}
	return append(s, it)
}

// cardFor 把缺失项映射到合规执行卡编号(与《合规落地执行卡》对齐)
func cardFor(items []model.ComplianceItem) string {
	out := ""
	for _, it := range items {
		switch it {
		case model.ItemICP:
			out += "执行卡3·备案流程; "
		case model.ItemTaxSource:
			out += "执行卡1·税源地合作模式; "
		case model.ItemEscrow:
			out += "执行卡2·支付托管选型; "
		case model.ItemKYC:
			out += "用户实名认证(onboarding); "
		}
	}
	return out
}

// Set 翻转平台级合规项(模拟合规线落地; 生产由真实签约状态驱动)
func (s *Service) Set(item model.ComplianceItem, on bool) { s.save(item, on) }

// SetKYCProvider 注入用户级 KYC 状态查询钩子(由 kyc 包的真实/模拟通道提供)。
// 注入后, KYC 闸门以 Provider 状态为准, 实现 KYC 从纯状态位到可查询 Provider 的迁移。
func (s *Service) SetKYCProvider(fn func(uid string) bool) { s.kycProvider = fn }

// GetState 读取当前合规状态(供 admin 查询)
func (s *Service) GetState() *model.ComplianceState { return s.load() }
