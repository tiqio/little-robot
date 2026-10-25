// Package sources 封装外部媒体源 API（当前只有 nhentai）。
//
// nhentai v2 客户端（2026-10 实测；v1 已废弃，/api/gallery/1 现在返回的是一句
// "Use new API https://nhentai.net/api/v2/docs" 文本，直接 JSON 解析会炸）：
//
//   - GET  /api/v2/cdn                          CDN 域名（动态！i1-i4 / t1-t4，缓存 24h）
//   - GET  /api/v2/galleries/{id}               详情：标题/标签/页面清单
//   - GET  /api/v2/search?query=&sort=&page=    搜索（匿名 10 次/分钟）
//   - POST /api/v2/galleries/{id}/download      官方 zip 链接（需要 API key：Key <api_key>）
//
// 页面图 URL = {image_server}/{pages[].path}，path 形如 "galleries/9/1.jpg"。
// search 的 query 语法很强：
//
//	"exact phrase"、-word、artist:name、language:english、tag:"big breasts"、
//	pages:>10、favorites:>=100、uploaded:<7d
package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"little-robot/internal/netutil"
)

const (
	baseURL = "https://nhentai.net"

	// 浏览器 UA：站子在 Cloudflare 后面，Go 默认 UA 偶尔被 403
	UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// 兜底 CDN：/api/v2/cdn 挂了就用这套（2026-10 实测值）
var (
	FallbackImageServers = []string{"https://i1.nhentai.net", "https://i2.nhentai.net", "https://i3.nhentai.net", "https://i4.nhentai.net"}
	FallbackThumbServers = []string{"https://t1.nhentai.net", "https://t2.nhentai.net", "https://t3.nhentai.net", "https://t4.nhentai.net"}
)

// 老域名 v1 时代的单 CDN。实测（2026-10）：代理把 i1-i4 的路由搞挂时，
// i.nhentai.net 的完整页经常还活着，放在阶梯最末端当保险丝。
const LegacyImageServer = "https://i.nhentai.net"

// Tag 本子标签（artist / character / copyright / meta / ...）
type Tag struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// Page 单页
type Page struct {
	Number   int
	URL      string // 完整 URL：{image_server}/{path}
	ThumbURL string // 缩略图完整 URL
	Path     string // 相对路径："galleries/9/1.jpg"（CDN 域名会变，路径不变）
	Width    int
	Height   int
}

// Gallery 详情
type Gallery struct {
	ID            int
	MediaID       string
	EnglishTitle  string
	JapaneseTitle string
	Scanlator     string
	UploadDate    time.Time
	NumPages      int
	NumFavorites  int
	Tags          []Tag
	Pages         []Page
	CoverPath     string
	CoverURL      string
	ThumbURL      string
}

// GallerySummary 搜索结果条目
type GallerySummary struct {
	ID            int
	MediaID       string
	EnglishTitle  string
	JapaneseTitle string
	ThumbPath     string
	ThumbURL      string
	NumPages      int
	NumFavorites  int
}

type cdnConfig struct {
	Image []string
	Thumb []string
}

// Nhentai v2 客户端
type Nhentai struct {
	apiKey string

	mu      sync.Mutex
	cdn     cdnConfig
	cdnTime time.Time
}

func NewNhentai(apiKey string) *Nhentai {
	return &Nhentai{apiKey: strings.TrimSpace(apiKey)}
}

// HasKey 有没有配 API key（决定能否用官方 zip 下载接口）
func (c *Nhentai) HasKey() bool { return c.apiKey != "" }

// CDN 返回图片/缩略图 CDN 域名列表。拉 /api/v2/cdn 缓存 24h，失败用兜底值（不致命）。
func (c *Nhentai) CDN(ctx context.Context) (image, thumb []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cdn.Image != nil && time.Since(c.cdnTime) < 24*time.Hour {
		return c.cdn.Image, c.cdn.Thumb
	}

	data, _, err := netutil.FetchWith(ctx, baseURL+"/api/v2/cdn", netutil.FetchOpt{UA: UserAgent, MaxBytes: 1 << 20})
	if err == nil {
		var cfg struct {
			ImageServers []string `json:"image_servers"`
			ThumbServers []string `json:"thumb_servers"`
		}
		if json.Unmarshal(data, &cfg) == nil && len(cfg.ImageServers) > 0 {
			c.cdn = cdnConfig{Image: cfg.ImageServers, Thumb: cfg.ThumbServers}
			c.cdnTime = time.Now()
			return c.cdn.Image, c.cdn.Thumb
		}
	}
	return FallbackImageServers, FallbackThumbServers
}

func pick(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[rand.Intn(len(list))]
}

func head(b []byte) string {
	const n = 120
	if len(b) < n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// Gallery 拉详情（含全部页面清单）。id 是数字 id（"123-abc" 里的 123）。
func (c *Nhentai) Gallery(ctx context.Context, id int) (*Gallery, error) {
	data, _, err := netutil.FetchWith(ctx, fmt.Sprintf("%s/api/v2/galleries/%d", baseURL, id),
		netutil.FetchOpt{UA: UserAgent, MaxBytes: 8 << 20})
	if err != nil {
		return nil, fmt.Errorf("拉取 nhentai 详情失败（id=%d）：%w", id, err)
	}
	// 防御：v1 式站点返回纯文本（"Use new API ..."）时 JSON 解析会莫名失败
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return nil, fmt.Errorf("nhentai 返回了非 JSON 内容（id=%d）：%s", id, head(data))
	}

	var raw struct {
		ID      int    `json:"id"`
		MediaID string `json:"media_id"`
		Title   struct {
			English  string `json:"english"`
			Japanese string `json:"japanese"`
		} `json:"title"`
		Cover struct {
			Path string `json:"path"`
		} `json:"cover"`
		Thumbnail struct {
			Path string `json:"path"`
		} `json:"thumbnail"`
		Scanlator    string `json:"scanlator"`
		UploadDate   int64  `json:"upload_date"`
		NumPages     int    `json:"num_pages"`
		NumFavorites int    `json:"num_favorites"`
		Tags         []Tag  `json:"tags"`
		Pages        []struct {
			Number int    `json:"number"`
			Path   string `json:"path"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
			Thumb  string `json:"thumbnail"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("解析 nhentai 详情失败（id=%d）：%w", id, err)
	}

	imageCDN, thumbCDN := c.CDN(ctx)
	g := &Gallery{
		ID:            raw.ID,
		MediaID:       raw.MediaID,
		EnglishTitle:  raw.Title.English,
		JapaneseTitle: raw.Title.Japanese,
		Scanlator:     raw.Scanlator,
		UploadDate:    time.Unix(raw.UploadDate, 0),
		NumPages:      raw.NumPages,
		NumFavorites:  raw.NumFavorites,
		Tags:          raw.Tags,
		CoverPath:     raw.Cover.Path,
		CoverURL:      pick(imageCDN) + "/" + raw.Cover.Path,
		ThumbURL:      pick(thumbCDN) + "/" + raw.Thumbnail.Path,
	}
	for _, p := range raw.Pages {
		g.Pages = append(g.Pages, Page{
			Number:   p.Number,
			Path:     p.Path,
			URL:      pick(imageCDN) + "/" + p.Path,
			ThumbURL: pick(thumbCDN) + "/" + p.Thumb,
			Width:    p.Width,
			Height:   p.Height,
		})
	}
	if len(g.Pages) == 0 {
		return nil, fmt.Errorf("nhentai 详情里没有页面（id=%d，num_pages=%d）", id, raw.NumPages)
	}
	return g, nil
}

// Search 搜索本子。sort 固定 date（v2 还接受 popular / popular-today / popular-week / popular-month）。
// 返回第一页结果 + 总条数（匿名限流 10 次/分钟，别拿来翻页刷）。
func (c *Nhentai) Search(ctx context.Context, query string) (items []GallerySummary, total int, err error) {
	q := url.Values{}
	q.Set("query", strings.TrimSpace(query))
	q.Set("sort", "date")
	q.Set("page", "1")

	data, _, err := netutil.FetchWith(ctx, baseURL+"/api/v2/search?"+q.Encode(),
		netutil.FetchOpt{UA: UserAgent, MaxBytes: 4 << 20})
	if err != nil {
		return nil, 0, fmt.Errorf("nhentai 搜索失败（%q）：%w", query, err)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return nil, 0, fmt.Errorf("nhentai 搜索返回了非 JSON 内容：%s", head(data))
	}

	return c.parseGalleryList(ctx, data, "搜索响应")
}

// Latest 拉"最新本子列表"——官网 https://nhentai.net/?page=N 的 JSON 版
// （按上传时间倒序，25 条/页起，匿名 15 次/分钟）。比直接爬 HTML 页稳：
// HTML 结构一变就炸，JSON 有 OpenAPI 契约。
func (c *Nhentai) Latest(ctx context.Context, page, perPage int) (items []GallerySummary, total int, err error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 25
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(perPage))

	data, _, err := netutil.FetchWith(ctx, baseURL+"/api/v2/galleries?"+q.Encode(),
		netutil.FetchOpt{UA: UserAgent, MaxBytes: 4 << 20})
	if err != nil {
		return nil, 0, fmt.Errorf("nhentai 最新列表拉取失败（page=%d）：%w", page, err)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return nil, 0, fmt.Errorf("nhentai 最新列表返回了非 JSON 内容：%s", head(data))
	}
	return c.parseGalleryList(ctx, data, "最新列表响应")
}

// parseGalleryList 解析 search / galleries 共用的 {result: [...], total} 结构
func (c *Nhentai) parseGalleryList(ctx context.Context, data []byte, what string) (items []GallerySummary, total int, err error) {
	var raw struct {
		Result []struct {
			ID            int    `json:"id"`
			MediaID       string `json:"media_id"`
			EnglishTitle  string `json:"english_title"`
			JapaneseTitle string `json:"japanese_title"`
			Thumbnail     string `json:"thumbnail"`
			NumPages      int    `json:"num_pages"`
			NumFavorites  int    `json:"num_favorites"`
		} `json:"result"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, 0, fmt.Errorf("解析 nhentai %s失败：%w", what, err)
	}

	_, thumbCDN := c.CDN(ctx)
	for _, r := range raw.Result {
		items = append(items, GallerySummary{
			ID:            r.ID,
			MediaID:       r.MediaID,
			EnglishTitle:  r.EnglishTitle,
			JapaneseTitle: r.JapaneseTitle,
			ThumbPath:     r.Thumbnail,
			ThumbURL:      pick(thumbCDN) + "/" + r.Thumbnail,
			NumPages:      r.NumPages,
			NumFavorites:  r.NumFavorites,
		})
	}
	return items, raw.Total, nil
}

// FetchThumb 下载搜索结果缩略图（逐个缩略图 CDN 试，t.nhentai.net 老域名垫底）
func (c *Nhentai) FetchThumb(ctx context.Context, path string) ([]byte, error) {
	thumb, _ := c.CDN(ctx)
	ladder := append(append([]string{}, thumb...), "https://t.nhentai.net")
	var lastErr error
	for _, srv := range ladder {
		data, _, err := netutil.FetchWith(ctx, srv+"/"+path, netutil.FetchOpt{UA: UserAgent, MaxBytes: 1 << 20})
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("%d 个缩略图 CDN 都失败（%s）：%w", len(ladder), path, lastErr)
}

// DownloadURL 拿官方下载链接（zip / cbz / torrent）。
// 需要 API key（.env 的 NHENTAI_API_KEY，后台「API Keys」页创建）；
// 没有 key 或接口被关（503 Feature is currently disabled）就返回错误，调用方降级为逐页下载。
func (c *Nhentai) DownloadURL(ctx context.Context, id int, format string) (string, time.Time, error) {
	reqURL := fmt.Sprintf("%s/api/v2/galleries/%d/download?format=%s", baseURL, id, format)
	req, err := netutil.NewRequest(ctx, reqURL)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("构造 nhentai 下载请求失败：%w", err)
	}
	req.Method = "POST"
	req.Header.Set("User-Agent", UserAgent)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Key "+c.apiKey)
	}

	data, err := netutil.DoJSON(req, 1<<20)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("nhentai 官方下载接口失败（id=%d）：%w", id, err)
	}
	var raw struct {
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", time.Time{}, fmt.Errorf("解析 nhentai 下载链接失败：%w", err)
	}
	if raw.URL == "" {
		return "", time.Time{}, fmt.Errorf("nhentai 返回的下载链接为空（id=%d）", id)
	}
	return raw.URL, time.Unix(raw.ExpiresAt, 0), nil
}

// fetchPath 按相对路径下图，走 CDN 阶梯：
//
//	第一轮：i1-i4（/api/v2/cdn 实时值，随机顺序）+ i.nhentai.net（v1 老域名）
//	第二轮：歇 500ms 再来一遍（代理换节点往往需要一点时间）
//
// 为什么逐个试：代理（Clash 等）对单条 HTTP/2 连接偶发 PROTOCOL_ERROR，
// 换个 CDN 域名 = 换一条连接/一个出口 IP，实测多域名里总有一个活着。
func (c *Nhentai) fetchPath(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	image, _ := c.CDN(ctx)
	ladder := append(append([]string{}, image...), LegacyImageServer)

	var lastErr error
	for round := 0; round < 2; round++ {
		order := ladder
		if round == 1 {
			// 第二轮倒着来：代理换节点后，之前最后试的那个域名反而可能先活
			order = make([]string, len(ladder))
			for i, s := range ladder {
				order[len(ladder)-1-i] = s
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		for _, srv := range order {
			data, _, err := netutil.FetchWith(ctx, srv+"/"+path, netutil.FetchOpt{UA: UserAgent, MaxBytes: maxBytes})
			if err == nil {
				return data, nil
			}
			lastErr = err
		}
	}
	return nil, fmt.Errorf("%d 个 CDN 试了两轮都失败（%s）：%w", len(ladder), path, lastErr)
}

// FetchPage 下载单页图（12MB 上限，页面图一般 <1MB）
func (c *Nhentai) FetchPage(ctx context.Context, p Page) ([]byte, error) {
	data, err := c.fetchPath(ctx, p.Path, 12<<20)
	if err != nil {
		return nil, fmt.Errorf("下载第 %d 页失败：%w", p.Number, err)
	}
	return data, nil
}

// FetchCover 下载封面
func (c *Nhentai) FetchCover(ctx context.Context, g *Gallery) ([]byte, error) {
	data, err := c.fetchPath(ctx, g.CoverPath, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("下载封面失败：%w", err)
	}
	return data, nil
}

// ParseGalleryID 把 "12345" / "123-abc" / "https://nhentai.net/g/12345/" 都解析成数字 id。
// nhentai 的 code 形如 "123-abc"，破折号前的数字部分就是 id。
func ParseGalleryID(raw string) (int, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(s, "nhentai.net/g/"); i >= 0 {
		s = s[i+len("nhentai.net/g/"):]
	} else if i := strings.Index(s, "nhentai.net/gallery/"); i >= 0 {
		s = s[i+len("nhentai.net/gallery/"):]
	}
	s = strings.Trim(s, "/")
	if i := strings.Index(s, "?"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "-"); i > 0 {
		s = s[:i]
	}
	id, err := strconv.Atoi(s)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("解析不出 nhentai id：%q", raw)
	}
	return id, nil
}
