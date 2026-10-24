package guard

import (
	"context"
	"fmt"
)

// DenyApprover 用于无人值守场景（飞书/定时任务），默认值是拒绝不是放行，这是“最小权限原则”
// 高危操作一律拒绝，并把原因告诉模型，让它换一条安全的路
type DenyApprover struct {
	Reason string
}

func (a *DenyApprover) Ask(ctx context.Context, toolName, args, reason string) (bool, error) {
	// 全部拒绝，没有reason就加reason
	if a.Reason == "" {
		a.Reason = "当前为无人值守环境，高危操作需人工执行"
	}
	return false, fmt.Errorf("%s（操作：%s）", a.Reason, reason)
}
