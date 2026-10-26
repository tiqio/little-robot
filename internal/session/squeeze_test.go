package session

import (
	"strings"
	"testing"

	"little-robot/internal/schema"
)

// 真实事故：保护区只有 3 条，但这 3 条里塞着几十万字符（图片 data URL），
// 旧实现压完只掉 280 字符，模型预填充 38 分钟。
func TestSqueezeToBudgetWithHugeProtected(t *testing.T) {
	c := NewCompactor(0, 3)
	msgs := []schema.Message{{Role: schema.RoleSystem, Content: "系统提示"}}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, schema.Message{Role: schema.RoleUser, ToolCallID: "c", Content: "小结果"})
	}
	// 最后两条带巨型 data URL（保护区里）
	huge := "data:image/jpeg;base64," + strings.Repeat("A", 150000)
	msgs = append(msgs,
		schema.Message{Role: schema.RoleUser, Content: "（系统注入）看图", Images: []string{huge, huge}},
		schema.Message{Role: schema.RoleUser, Content: "最后一句"},
	)

	budget := 30000
	out, folded := c.SqueezeTo(msgs, budget)
	got := estimateOf(out)
	if got > budget {
		t.Errorf("压缩后 %d 字符，超过预算 %d", got, budget)
	}
	if !folded {
		t.Log("这次没走到折叠兜底（剥图就够）")
	}
	// 图片必须被剥掉
	for _, m := range out {
		if len(m.Images) > 0 {
			t.Error("图片应该被剥掉")
		}
	}
}

// 极端情况：连剥图+紧压都不够 → 折叠成一条摘要，保证能发出去
func TestSqueezeToFoldsWhenStillTooBig(t *testing.T) {
	c := NewCompactor(0, 3)
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "系统"},
		{Role: schema.RoleUser, Content: strings.Repeat("巨长", 200000)}, // 40 万字符的单条消息
		{Role: schema.RoleAssistant, Content: "好的"},
	}
	out, _ := c.SqueezeTo(msgs, 5000)
	if estimateOf(out) > 5000 {
		t.Errorf("压完还有 %d 字符，超过预算", estimateOf(out))
	}
	// 单条巨型 user 消息现在会被折叠成占位符，所以未必需要"摘要兜底"，
	// 关键要求是**压进预算**（哪种手段都行）
	if estimateOf(out) > 5000 {
		t.Errorf("压缩后必须 ≤ 预算")
	}
	if len(out) == 0 {
		t.Fatal("不能把历史压没了")
	}
}

// 折叠要保住最后一条用户消息（用户当前的问题不能丢）
func TestSummarizeKeepsLastUser(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: strings.Repeat("旧", 5000)},
		{Role: schema.RoleAssistant, Content: "旧回答"},
		{Role: schema.RoleUser, Content: "帮我找爱弥斯的本子"},
	}
	out := Summarize(msgs, 2000)
	if !strings.Contains(out[1].Content, "帮我找爱弥斯的本子") {
		t.Errorf("最后一条用户消息必须保留：%q", out[1].Content)
	}
	if !strings.Contains(out[1].Content, "省略") {
		t.Errorf("要说明省略了多少条：%q", out[1].Content)
	}
}

// 诊断输出要能指出"大块头是谁"
func TestTopMessages(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "小"},
		{Role: schema.RoleUser, Content: strings.Repeat("x", 1000)},
		{Role: schema.RoleUser, Images: []string{strings.Repeat("y", 2000)}},
	}
	top := TopMessages(msgs, 2)
	if len(top) != 2 {
		t.Fatalf("应该返回 2 条，实际 %d", len(top))
	}
	if !strings.Contains(top[0], "2000") {
		t.Errorf("最大的应该是带图那条：%v", top)
	}
}
