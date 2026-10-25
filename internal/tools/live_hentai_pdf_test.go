package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"little-robot/internal/sources"
)

// 真机（NETUTIL_LIVE=1）：抓 684400，默认 format 生成 PDF
func TestLiveHentaiPDF(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("需要 NETUTIL_LIVE=1")
	}
	dir := "/tmp/live_hentai" // 固定路径，方便外部校验 PDF
	_ = os.MkdirAll(dir, 0o755)
	tool := NewFetchHentaiTool(dir, sources.NewNhentai(os.Getenv("NHENTAI_API_KEY")))

	out, err := tool.Execute(context.Background(), []byte(`{"id_or_url":"684400","format":"pdf"}`))
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	t.Logf("返回：\n%s", out)

	// 断言 PDF 真生成了
	pattern := dir + "/out/hentai/684400/*.pdf"
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatalf("没有生成 PDF：%s", pattern)
	}
	for _, m := range matches {
		fi, _ := os.Stat(m)
		data, _ := os.ReadFile(m)
		t.Logf("%s：%.1fMB，头 %q", m, float64(fi.Size())/1e6, string(data[:8]))
	}
}
