package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"little-robot/internal/schema"
	"little-robot/internal/sources"
)

// live：fetch_hentai 整本 zip（gallery 1 只有 14 页，够小）
func TestFetchHentaiZipLive(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("设置 NETUTIL_LIVE=1 才跑")
	}
	dir := t.TempDir()
	reg := NewRegistry()
	reg.Register(NewFetchHentaiTool(dir, sources.NewNhentai(os.Getenv("NHENTAI_API_KEY"))))

	res := reg.Execute(context.Background(), schema.ToolCall{
		ID:        "h1",
		Name:      "fetch_hentai",
		Arguments: json.RawMessage(`{"id_or_url":"https://nhentai.net/g/1/","format":"zip"}`),
	})
	if res.IsError {
		t.Fatalf("执行失败：%s", res.Output)
	}
	t.Logf("输出：\n%s", res.Output)
	if len(res.Images) == 0 {
		t.Error("期望回传封面图")
	}
	// 工作区里应该有 cover + zip
	entries, _ := os.ReadDir(filepath.Join(dir, "out", "hentai", "1"))
	t.Logf("工作区文件：%v", names(entries))
}

// live：fetch_hentai 搜索模式
func TestFetchHentaiSearchLive(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("设置 NETUTIL_LIVE=1 才跑")
	}
	dir := t.TempDir()
	reg := NewRegistry()
	reg.Register(NewFetchHentaiTool(dir, sources.NewNhentai("")))

	res := reg.Execute(context.Background(), schema.ToolCall{
		ID:        "h2",
		Name:      "fetch_hentai",
		Arguments: json.RawMessage(`{"query":"sonohara"}`),
	})
	if res.IsError {
		t.Fatalf("执行失败：%s", res.Output)
	}
	t.Logf("输出：\n%s", res.Output)
	t.Logf("回传封面数：%d（期望 ≤3）", len(res.Images))
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// live：fetch_hentai 最新列表（官网 ?page=N 的 JSON 版）
func TestFetchHentaiLatestLive(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("设置 NETUTIL_LIVE=1 才跑")
	}
	dir := t.TempDir()
	reg := NewRegistry()
	reg.Register(NewFetchHentaiTool(dir, sources.NewNhentai("")))

	res := reg.Execute(context.Background(), schema.ToolCall{
		ID:        "h3",
		Name:      "fetch_hentai",
		Arguments: json.RawMessage(`{"page":6458}`),
	})
	if res.IsError {
		t.Fatalf("执行失败：%s", res.Output)
	}
	t.Logf("输出：\n%s", res.Output)
}

// live：fetch_image rating=explicit（yande 主力）
func TestImageToolR18Live(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("设置 NETUTIL_LIVE=1 才跑")
	}
	dir := t.TempDir()
	reg := NewRegistry()
	reg.Register(NewImageTool(dir, ""))

	res := reg.Execute(context.Background(), schema.ToolCall{
		ID:        "r1",
		Name:      "fetch_image",
		Arguments: json.RawMessage(`{"keyword":"cat_ears","source":"anime","rating":"explicit","path":"out/r18.jpg"}`),
	})
	if res.IsError {
		t.Fatalf("执行失败：%s", res.Output)
	}
	t.Logf("输出：\n%s", res.Output)
	if len(res.Images) == 0 {
		t.Error("期望回传图片")
	}
}
