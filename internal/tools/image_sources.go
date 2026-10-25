package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"little-robot/internal/netutil"
)

/*
NSFW 图源（与 Danbooru 同协议的 Booru 家族）。

实测结论（2026-10-25）：
- yande.re：最大的 NSFW 标签库，纯 HTTP 免 key，/post.json?tags=...&rating=s|q|e
- konachan.com：Danbooru 系，但站在 Cloudflare JS challenge 后面，纯 HTTP 客户端
  偶尔被 "Just a moment..." 拦 → 只配在链路末位兜底
- sankarea.eu：yande 的镜像，协议相同，yande 抽风时顶上

三家响应结构一致：[{id, tags, file_url, sample_url, preview_url, file_size, ...}]
rating：s=safe，q=questionable，e=explicit（纯 R18）
*/

// booruRating 把对外的 rating 枚举映射成 booru 的 s/q/e
func booruRating(rating string) string {
	switch rating {
	case "questionable":
		return "q"
	case "explicit":
		return "e"
	default:
		return "s"
	}
}

// searchBooru 通用 Booru 抓取：yande / konachan / sankarea 共用
func searchBooru(ctx context.Context, name, base, keyword, rating string) ([]imageCandidate, error) {
	tags := strings.ReplaceAll(strings.TrimSpace(keyword), " ", "_")
	q := url.Values{}
	q.Set("tags", tags)
	q.Set("rating", booruRating(rating))
	q.Set("limit", "8")

	data, _, err := netutil.Fetch(ctx, base+"/post.json?"+q.Encode(), 4<<20)
	if err != nil {
		return nil, fmt.Errorf("%s 搜索失败：%w", name, err)
	}

	var posts []struct {
		Tags      string `json:"tags"`
		FileURL   string `json:"file_url"`
		SampleURL string `json:"sample_url"`
		FileSize  int64  `json:"file_size"`
	}
	if err := json.Unmarshal(data, &posts); err != nil {
		return nil, fmt.Errorf("解析 %s 响应失败：%w", name, err)
	}

	var out []imageCandidate
	for _, p := range posts {
		u := p.FileURL
		// 原图超过 8MB 时改用 sample（缩放版）：飞书图片上限 10MB，留点余量
		if u != "" && p.FileSize > 8<<20 && p.SampleURL != "" {
			u = p.SampleURL
		}
		if u == "" || !strings.HasPrefix(u, "https://") {
			continue
		}
		src := name
		if p.Tags != "" {
			t := p.Tags
			if len(t) > 60 {
				t = t[:60] + "…"
			}
			src += "（" + t + "）"
		}
		out = append(out, imageCandidate{
			URL:     u,
			Source:  src,
			License: "同人图，注意版权",
		})
	}
	return out, nil
}

func searchYande(ctx context.Context, keyword, rating string) ([]imageCandidate, error) {
	return searchBooru(ctx, "yande.re", "https://yande.re", keyword, rating)
}

func searchKonachan(ctx context.Context, keyword, rating string) ([]imageCandidate, error) {
	return searchBooru(ctx, "Konachan", "https://konachan.com", keyword, rating)
}

func searchSankarea(ctx context.Context, keyword, rating string) ([]imageCandidate, error) {
	return searchBooru(ctx, "Sankarea", "https://sankarea.eu", keyword, rating)
}
