package session

import (
	"strings"
	"testing"

	"little-robot/internal/schema"
)

// 老图片消息要剥掉 data URL 留占位，保护区内的保留
func TestCompactorStripsOldImages(t *testing.T) {
	c := NewCompactor(6000, 4) // 阈值调小保证触发压缩（两张 4KB 图 + 文字 ≈ 8KB），保护区 4 条

	// 构造 10 条消息：第 2 条是带图的（保护区外），最后 3 条里有 1 条带图的（保护区内）
	msgs := make([]schema.Message, 10)
	msgs[0] = schema.Message{Role: schema.RoleSystem, Content: "system"}
	msgs[2] = schema.Message{Role: schema.RoleUser, Content: "", Images: []string{"data:image/jpeg;base64," + strings.Repeat("A", 4000)}}
	msgs[9] = schema.Message{Role: schema.RoleUser, Content: "最新看的图", Images: []string{"data:image/jpeg;base64," + strings.Repeat("B", 4000)}}

	out := c.Compact(msgs)

	// 老图：剥掉，留占位
	if len(out[2].Images) != 0 {
		t.Errorf("保护区外的老图应该被剥掉，还剩 %d 张", len(out[2].Images))
	}
	if !strings.Contains(out[2].Content, "查看过 1 张图片") {
		t.Errorf("老图应该留占位说明，实际 content=%q", out[2].Content)
	}

	// 新图：保留
	if len(out[9].Images) != 1 {
		t.Errorf("保护区内的图应该保留，实际 %d 张", len(out[9].Images))
	}
	if out[9].Content != "最新看的图" {
		t.Errorf("保护区内的图不应该被改 content，实际 %q", out[9].Content)
	}

	// 原消息不动（压的是快照）
	if len(msgs[2].Images) != 1 {
		t.Error("Compact 不应该改原始历史（应该压副本）")
	}
}

// estimate 要把图片字节算进去：否则历史里躺 3 张图，阈值永远不触发
func TestCompactorEstimateCountsImages(t *testing.T) {
	c := NewCompactor(1000, 4)

	plain := []schema.Message{{Role: schema.RoleUser, Content: "hello"}}
	withImg := []schema.Message{{Role: schema.RoleUser, Content: "hello", Images: []string{"data:image/jpeg;base64," + strings.Repeat("A", 5000)}}}

	if c.estimate(withImg) <= c.estimate(plain) {
		t.Errorf("带图的历史应该估出更大的体积：%d vs %d", c.estimate(withImg), c.estimate(plain))
	}
}

// 保护区里也只保留最近 2 条带图消息：图片是最贵的上下文消耗
func TestCompactorKeepsOnlyRecentImages(t *testing.T) {
	c := NewCompactor(2000, 36) // 保护区覆盖全部消息
	img := func() []string { return []string{"data:image/jpeg;base64," + strings.Repeat("A", 3000)} }

	var msgs []schema.Message
	msgs = append(msgs, schema.Message{Role: schema.RoleUser, Content: "用户问", Images: img()})
	for i := 0; i < 3; i++ {
		msgs = append(msgs, schema.Message{Role: schema.RoleUser, Content: "看这张", Images: img()})
	}

	out := c.Compact(msgs)
	kept := 0
	for _, m := range out {
		if len(m.Images) > 0 {
			kept++
		}
	}
	if kept != 2 {
		t.Errorf("应该只保留最近 2 条带图消息，实际 %d 条", kept)
	}
	// 被剥掉的那几条要留占位说明
	if !strings.Contains(out[0].Content, "查看过 1 张图片") {
		t.Errorf("老图应该留占位：%q", out[0].Content)
	}
	if len(out[len(out)-1].Images) == 0 {
		t.Error("最新一条的图必须保留（模型要看当前这张）")
	}
}
