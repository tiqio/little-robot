package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"little-robot/internal/schema"
	"little-robot/internal/sources"
)

// 图片来源记录：同一 URL 第二次要直接复用，不再下载
func TestImageSourceReuse(t *testing.T) {
	dir := t.TempDir()
	l := NewSentLedger("")
	url := "https://example.com/a.jpg"
	if err := l.MarkSource(url, "out/images/a.jpg"); err != nil {
		t.Fatal(err)
	}
	prev, at, ok := l.SeenSource(url)
	if !ok {
		t.Fatal("登记后应该认为下载过")
	}
	if prev != "out/images/a.jpg" || at.IsZero() {
		t.Errorf("应返回上次路径与时间：%q %v", prev, at)
	}
	// 不同 URL 不算
	if _, _, ok := l.SeenSource("https://example.com/b.jpg"); ok {
		t.Error("不同来源不该命中")
	}
	_ = dir
}

// 候选列表要标出"已拉过"的本子
func TestListModeMarksSeenGallery(t *testing.T) {
	dir := t.TempDir()
	tool := NewFetchHentaiTool(dir, sources.NewNhentai(""))
	if err := tool.ledger.MarkGallery("677912"); err != nil {
		t.Fatal(err)
	}
	items := []sources.GallerySummary{
		{ID: 677912, EnglishTitle: "旧的那本", NumPages: 18, NumFavorites: 100},
		{ID: 700001, EnglishTitle: "新的那本", NumPages: 20, NumFavorites: 50},
	}
	out, err := tool.listMode(context.Background(), dir, "nhentai 搜索 \"x\"", items, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已拉过") {
		t.Errorf("已拉过的本子应该有标记：\n%s", out)
	}
	// 标记只出现在旧的那条上
	lines := strings.Split(out, "\n")
	for _, ln := range lines {
		if strings.Contains(ln, "700001") && strings.Contains(ln, "已拉过") {
			t.Errorf("新本子不该被标记：%s", ln)
		}
	}
}

// 重复抓同一本：详情返回里要有警告
func TestDetailWarnsOnRepeat(t *testing.T) {
	dir := t.TempDir()
	l := sharedLedger(dir + "/.claw/sent.json")
	_ = l.MarkGallery("677912")
	// 只验证警告文本生成逻辑（真实抓取要联网，放在 live 测试里）
	warn := repeatGalleryWarning(l, 677912)
	if !strings.Contains(warn, "已经拉过") || !strings.Contains(warn, "677912") {
		t.Errorf("重复抓取要有明确警告：%q", warn)
	}
	if got := repeatGalleryWarning(l, 700001); got != "" {
		t.Errorf("没抓过的本子不该有警告：%q", got)
	}
}

var _ = schema.RoleUser
var _ = time.Now
