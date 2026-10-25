package sources

import (
	"context"
	"os"
	"testing"
)

// 纯函数：id 解析
func TestParseGalleryID(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"12345", 12345},
		{"12345 ", 12345},
		{"123-abc", 123},
		{"https://nhentai.net/g/12345/", 12345},
		{"https://nhentai.net/g/123-abc", 123},
		{"https://nhentai.net/gallery/777-x", 777},
		{"https://nhentai.net/g/999?version=2", 999},
		{"NHENTAI.NET/g/42/", 42}, // 大小写不敏感
	}
	for _, c := range cases {
		got, err := ParseGalleryID(c.in)
		if err != nil {
			t.Fatalf("ParseGalleryID(%q) 出错：%v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseGalleryID(%q) = %d，期望 %d", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"", "abc", "https://nhentai.net/g/", "-123", "12x45"} {
		if _, err := ParseGalleryID(bad); err == nil {
			t.Errorf("ParseGalleryID(%q) 期望失败，却成功了", bad)
		}
	}
}

func TestCDNFallback(t *testing.T) {
	// 没网也能拿到兜底值（CDN 内部吞掉错误）
	c := NewNhentai("")
	image, thumb := c.CDN(context.Background())
	if len(image) == 0 || len(thumb) == 0 {
		t.Fatalf("CDN 兜底失败：image=%v thumb=%v", image, thumb)
	}
}

// live：真实打 nhentai v2 API
func TestNhentaiLive(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("设置 NETUTIL_LIVE=1 才跑")
	}
	ctx := context.Background()
	c := NewNhentai(os.Getenv("NHENTAI_API_KEY"))

	image, thumb := c.CDN(ctx)
	t.Logf("CDN：image=%v thumb=%v", image, thumb)

	g, err := c.Gallery(ctx, 1) // 老书《Eat The Rich!》，14 页，稳定
	if err != nil {
		t.Fatalf("Gallery(1) 失败：%v", err)
	}
	t.Logf("详情：%s（%d 页，%d 标签，%d 收藏）", g.EnglishTitle, g.NumPages, len(g.Tags), g.NumFavorites)
	if g.NumPages != len(g.Pages) {
		t.Errorf("num_pages=%d 但 pages 清单只有 %d 条", g.NumPages, len(g.Pages))
	}
	if g.CoverURL == "" || g.Pages[0].URL == "" {
		t.Fatalf("封面/页面 URL 为空：cover=%s page0=%s", g.CoverURL, g.Pages[0].URL)
	}

	// 封面和页面走同一套 CDN 阶梯，但封面文件在 nhentai 侧偶尔整体降级
	// （2026-10 实测：所有 CDN 的 cover.jpg 全挂、页面正常）。工具层有
	// "第 1 页顶封面"的兜底，所以这里只要求"封面或第 1 页至少活一个"。
	cover, cerr := c.FetchCover(ctx, g)
	coverOK := cerr == nil && len(cover) > 0
	if !coverOK {
		t.Logf("封面挂了（工具层会用第 1 页顶）：%v", cerr)
	}
	page, perr := c.FetchPage(ctx, g.Pages[0])
	pageOK := perr == nil && len(page) > 0
	if !coverOK && !pageOK {
		t.Fatalf("封面和第 1 页都挂了：cover=%v page=%v", cerr, perr)
	}
	t.Logf("封面 %d 字节（%v），第 1 页 %d 字节", len(cover), coverOK, len(page))

	items, total, err := c.Search(ctx, "sonohara")
	if err != nil {
		t.Fatalf("Search 失败：%v", err)
	}
	t.Logf("搜索 sonohara：total=%d，本页 %d 条，首条 id=%d", total, len(items), firstID(items))
	if total == 0 {
		t.Error("sonohara 应该能搜到结果")
	}

	if c.HasKey() {
		zurl, exp, err := c.DownloadURL(ctx, 1, "zip")
		if err != nil {
			t.Logf("DownloadURL（官方 zip）失败，走逐页兜底：%v", err)
		} else {
			t.Logf("官方 zip：%s（%s 过期）", zurl, exp.Format("15:04"))
		}
	}
}

func firstID(items []GallerySummary) int {
	if len(items) == 0 {
		return -1
	}
	return items[0].ID
}
