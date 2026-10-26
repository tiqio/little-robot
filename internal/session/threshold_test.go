package session

import (
	"strings"
	"testing"

	"little-robot/internal/schema"
)

// 阈值调高后的行为：历史小就不压，历史大才压，且保护区保留 4 万字符原文
func TestThresholdBehavior(t *testing.T) {
	// 30% 窗口（131072*0.4*2 = 104857 字符）
	c := NewCompactor(104857, 36)
	mk := func(n int, text string) []schema.Message {
		msgs := []schema.Message{{Role: schema.RoleSystem, Content: "sys"}}
		for i := 0; i < n; i++ {
			msgs = append(msgs, schema.Message{Role: schema.RoleUser, ToolCallID: "c", Content: text})
		}
		return msgs
	}

	// 1) 两万字符：不该压
	small := mk(30, strings.Repeat("x", 600))
	if got := c.Compact(small); estimateOf(got) != estimateOf(small) {
		t.Errorf("低于阈值不该压缩：%d -> %d", estimateOf(small), estimateOf(got))
	}

	// 2) 二十万字符：要压，但保护区里的原文要留住
	big := mk(300, strings.Repeat("x", 700))
	before := estimateOf(big)
	out := c.Compact(big)
	after := estimateOf(out)
	if after >= before {
		t.Errorf("超过阈值应该压缩：%d -> %d", before, after)
	}
	// 最近几条必须是原文（未被替换成占位符）
	last := out[len(out)-1].Content
	if strings.Contains(last, "已清理") {
		t.Error("最近的工具结果不该被折叠")
	}
	// 保护区内原文总量应在 4 万字符量级（不是 0，也不是全部）
	var kept int
	for _, m := range out {
		if !strings.Contains(m.Content, "已清理") {
			kept += len(m.Content)
		}
	}
	if kept < 20000 {
		t.Errorf("保护区保留太少（%d 字符），模型会健忘", kept)
	}
	t.Logf("压缩：%d -> %d 字符，保护区保留 %d 字符", before, after, kept)
}
