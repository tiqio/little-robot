package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"little-robot/internal/schema"
)

// 验证：fetch_image 找图后，图片会通过 ToolResult.Images 回传给模型
func TestImageToolCarriesPicture(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("设置 NETUTIL_LIVE=1 才跑")
	}
	dir := t.TempDir()
	reg := NewRegistry()
	reg.Register(NewImageTool(dir, os.Getenv("PEXELS_API_KEY")))

	res := reg.Execute(context.Background(), schema.ToolCall{
		ID:        "1",
		Name:      "fetch_image",
		Arguments: json.RawMessage(`{"keyword":"golden retriever","path":"out/dog.jpg"}`),
	})

	if res.IsError {
		t.Fatalf("执行失败：%s", res.Output)
	}
	t.Logf("输出：\n%s", res.Output)
	t.Logf("回传给模型的图片数：%d", len(res.Images))
	if len(res.Images) == 0 {
		t.Fatal("期望至少一张图片回传（模型靠它判断对不对）")
	}
	if !strings.HasPrefix(res.Images[0], "data:image/") {
		t.Fatalf("不是 data URL：%.40s", res.Images[0])
	}
}

func TestImageToolAnime(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("设置 NETUTIL_LIVE=1 才跑")
	}
	dir := t.TempDir()
	reg := NewRegistry()
	reg.Register(NewImageTool(dir, ""))

	res := reg.Execute(context.Background(), schema.ToolCall{
		ID:        "2",
		Name:      "fetch_image",
		Arguments: json.RawMessage(`{"keyword":"cat_ears","source":"anime","path":"out/anime.jpg"}`),
	})
	if res.IsError {
		t.Fatalf("执行失败：%s", res.Output)
	}
	t.Logf("输出：\n%s", res.Output)
	t.Logf("回传给模型的图片数：%d", len(res.Images))
	if len(res.Images) == 0 {
		t.Fatal("期望回传图片")
	}
}
