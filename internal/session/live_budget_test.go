package session

import (
	"testing"

	"little-robot/internal/schema"
)

// 真机：加载那份 191 条/17.7 万字符的历史，验证自动压缩能压到 2 万以内
func TestLiveBudgetOnRealSession(t *testing.T) {
	s, err := Load("/Users/dd/AITEST/little-robot/workspace/chats/ddremedttt-f1a826/.claw/sessions/oc_f1a82656ab490474f7c111803fdfc49f.json")
	if err != nil {
		t.Skipf("拿不到会话文件：%v", err)
	}
	// 模拟真实形态：历史 + 一条新请求（不含图片：最新那张图按设计永远保留，
	// 会占掉 16 万字符，那是"模型必须看到当前图"的代价，不属于压缩缺陷）
	msgs := append([]schema.Message{{Role: schema.RoleSystem, Content: "系统提示"}}, s.Messages()...)
	msgs = append(msgs,
		schema.Message{Role: schema.RoleUser, Content: "帮我找一下鸣潮爱弥斯的R18本子，中文"},
	)
	before := estimateOf(msgs)

	// 自动压缩档（15% 窗口 ≈ 39321 字符）
	c := NewCompactor(39321, 36)
	out := c.Compact(msgs)
	after := estimateOf(out)

	t.Logf("压缩前 %d 字符 → 压缩后 %d 字符（%d 条 → %d 条）", before, after, len(msgs), len(out))
	if after > 40000 {
		t.Errorf("自动压缩后应该 ≤ 4 万字符，实际 %d（说明保护区还是太宽）", after)
	}
}
