package tools

import (
	"context"
	"os"
	"testing"

	"little-robot/internal/sources"
)

// 真机（NETUTIL_LIVE=1）：复现"aysa wuthering_waves chinese"撞 0 条的场景，
// 验证自动降级能命中。
func TestLiveHentaiSearchFallback(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("需要 NETUTIL_LIVE=1")
	}
	tool := NewFetchHentaiTool(t.TempDir(), sources.NewNhentai(os.Getenv("NHENTAI_API_KEY")))
	out, err := tool.Execute(context.Background(),
		[]byte(`{"query":"爱弥斯 鸣潮"}`))
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	t.Logf("返回：\n%s", out)
}
