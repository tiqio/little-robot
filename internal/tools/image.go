package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"little-robot/internal/netutil"
	"little-robot/internal/schema"
)

/*
fetch_image：一个工具，两种用法（找现成照片 / 下载指定 URL）。

关键设计：**下载完把图片本身回传给模型看**（images 字段），
让模型能"先看再决定"，而不是瞎下一张就发。
回传的图片会作为一条额外的 user 消息注入（厂商只允许 user 消息带图）。

图源优先级（实测可用）：
  1. Pexels      真实照片，需 PEXELS_API_KEY
  2. Wikimedia   CC 授权，免 key（必须带可识别 UA，否则 403）
  3. Openverse   聚合 Flickr 等 CC 图，免 key
  4. picsum      兜底随机图
AI 生成（pollinations）只在显式 url 时使用，不参与自动降级。
*/

// 图源对应的图片域（外加 AI 生成的 pollinations）
var imageHosts = []string{
	"images.pexels.com",     // Pexels
	"live.staticflickr.com", // Openverse 常见来源（Flickr CC）
	"upload.wikimedia.org",  // Wikimedia Commons
	"thumb.wikimedia.org",   // Wikimedia 缩略图
	"image.pollinations.ai", // AI 生成（仅显式 url 使用）
	"cdn.donmai.us",         // Danbooru 图床
	"safebooru.org",         // Safebooru
	"nekos.best",            // nekos.best 随机二次元图
	"yande.re",              // yande.re（NSFW 主力）
	"files.yande.re",        // yande 原图
	"assets.yande.re",       // yande 缩略图
	"konachan.com",          // Konachan（NSFW 兜底，CF challenge 偶尔拦人）
	"files.konachan.com",    // Konachan 图床
	"sankarea.eu",           // Sankarea（yande 镜像）
	"files.sankarea.eu",     // Sankarea 图床
	"i1.nhentai.net",        // nhentai CDN（/api/v2/cdn 实测 i1-i4，兜底硬编码）
	"i2.nhentai.net",
	"i3.nhentai.net",
	"i4.nhentai.net",
	"t1.nhentai.net", // nhentai 缩略图 CDN
	"t2.nhentai.net",
	"t3.nhentai.net",
	"t4.nhentai.net",
}

const imageMaxBytes = 10 << 20

var _ BaseTool = &ImageTool{}

type ImageTool struct {
	workDir   string
	pexelsKey string
	hosts     []string
}

func NewImageTool(workDir, pexelsKey string, extraHosts ...string) *ImageTool {
	hosts := append([]string{}, imageHosts...)
	hosts = append(hosts, extraHosts...)
	return &ImageTool{workDir: workDir, pexelsKey: pexelsKey, hosts: hosts}
}

func (t *ImageTool) Name() string { return "fetch_image" }

func (t *ImageTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: `获取一张图片存到工作区，并把图片回传给你"看"，随后用 send_image 发送。

用法（二选一）：
A. keyword：按关键词搜现成照片，只用三个可信源，依次尝试：
   Pexels（质量最好，需 key）→ Openverse（Flickr 等 CC 实拍）→ Wikimedia Commons（CC）。
   关键词用英文、具体（golden retriever、office desk、server room）。
   三个源都找不到就返回失败，**不会拿无关的随机图凑数**。
B. url：下载指定 https 直链（域名需白名单）。
   只有用户明确要求"生成/画"时，才用 https://image.pollinations.ai/prompt/<英文描述>。

source=anime（二次元）：走 yande.re/Danbooru/Safebooru/nekos.best/Konachan。
   - keyword **只要给角色名或一个特征**（nahida、cat_ears、blue_hair），
     工具会自动解析成规范标签（nahida → nahida_(genshin_impact)）。
   - 不要堆叠多个条件：Danbooru 匿名搜索最多 2 个标签，写多了只会得到空结果，
     而"空结果"不代表没这张图，只代表标签组合太窄。
   - rating 控制尺度（默认 general）：
     general=SFW；questionable=轻度 R；explicit=纯 R18（用户说"色图/r18/福利"就用 explicit）。
     explicit 时主力切到 yande.re（最大 NSFW 库），Danbooru 按 rating:explicit 过滤。
   - 返回里会写「实际标签：xxx」，看清它再去调整关键词，不要盲目换词重试。

【必须看图确认】拿到图后你要先判断它是否符合用户要求：
- 符合 → 调 send_image 发送；
- 不符合（内容不对、太糊、明显是无关图）→ 换下一个候选 url 重新 fetch_image，
  或换更具体的英文关键词。最多换 3 次，仍不行就如实告诉用户没找到合适的。
- 返回里会列出同批候选，可以直接用它们的 url 重试，不要自己编 URL。

限制：jpg/png/webp/gif，≤10MB；SVG 不能直接发（需转 PNG）。回复用户时说明来源。`,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"keyword": map[string]interface{}{
					"type":        "string",
					"description": "英文关键词，用于搜现成照片；与 url 二选一",
				},
				"url": map[string]interface{}{
					"type":        "string",
					"description": "图片直链（https）；与 keyword 二选一",
				},
				"path": map[string]interface{}{
					"type":        "string",
					"description": "保存到工作区的相对路径，例如 out/photo.jpg",
				},
				"source": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"photo", "anime"},
					"description": "photo=真实照片（Pexels/Openverse/Wikimedia）；anime=二次元（yande/Danbooru/Safebooru/nekos.best），关键词用标签风格如 cat_ears、blue_hair。默认 photo",
				},
				"rating": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"general", "questionable", "explicit"},
					"description": "anime 专用：general=SFW（默认）；questionable=轻度 R；explicit=纯 R18。photo 源忽略此参数",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (t *ImageTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Keyword string `json:"keyword"`
		URL     string `json:"url"`
		Path    string `json:"path"`
		Source  string `json:"source"`
		Rating  string `json:"rating"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%w", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	if strings.TrimSpace(in.Keyword) == "" && strings.TrimSpace(in.URL) == "" {
		return "", fmt.Errorf("keyword 和 url 至少给一个")
	}

	dir := effectiveWorkDir(ctx, t.workDir)
	full, err := resolvePath(dir, in.Path)
	if err != nil {
		return "", err
	}

	// B. 指定 URL
	if strings.TrimSpace(in.URL) != "" {
		out, err := t.download(ctx, in.URL, in.Path, full, "指定 URL")
		if err != nil {
			return "", err
		}
		return out + "\n（图片已附上，请先确认是否符合要求再发送）", nil
	}

	// A. 按关键词，多候选
	keyword := strings.TrimSpace(in.Keyword)
	source := strings.ToLower(strings.TrimSpace(in.Source))
	if source == "" {
		source = "photo"
	}
	rating := strings.ToLower(strings.TrimSpace(in.Rating))
	if rating == "" {
		rating = "general"
	}
	switch rating {
	case "general", "questionable", "explicit":
	default:
		return "", fmt.Errorf("rating 只能是 general/questionable/explicit，收到 %q", in.Rating)
	}

	var chain []func(context.Context, string) ([]imageCandidate, error)
	switch source {
	case "photo":
		// 真实照片：Pexels 质量最好 → Openverse(Flickr CC) → Wikimedia(CC)
		chain = []func(context.Context, string) ([]imageCandidate, error){
			t.searchPexels, searchOpenverse, searchCommons,
		}
	case "anime":
		// 二次元：Danbooru 标签最准 → Safebooru → nekos 随机兜底
		chain = []func(context.Context, string) ([]imageCandidate, error){
			func(ctx context.Context, kw string) ([]imageCandidate, error) { return searchDanbooru(ctx, kw, rating) },
			searchSafebooru, searchNekosBest,
		}
		if rating != "general" {
			// R18：yande.re 最大 NSFW 库打头，Konachan 殿后兜底（CF challenge 偶尔拦人）
			chain = append([]func(context.Context, string) ([]imageCandidate, error){
				func(ctx context.Context, kw string) ([]imageCandidate, error) { return searchYande(ctx, kw, rating) },
			}, chain...)
			chain = append(chain,
				func(ctx context.Context, kw string) ([]imageCandidate, error) { return searchKonachan(ctx, kw, rating) })
		}
	default:
		return "", fmt.Errorf("source 只能是 photo 或 anime，收到 %q", in.Source)
	}

	var notes []string
	for _, find := range chain {
		cands, err := find(ctx, keyword)
		if err != nil {
			notes = append(notes, err.Error())
			continue
		}
		if len(cands) == 0 {
			continue
		}

		out, derr := t.download(ctx, cands[0].URL, in.Path, full, cands[0].Source+"；授权："+cands[0].License)
		if derr != nil {
			notes = append(notes, cands[0].Source+" 下载失败："+derr.Error())
			continue
		}

		var b strings.Builder
		b.WriteString(out)
		b.WriteString("\n\n【先看图确认】符合要求再 send_image；不符合就换下面候选重试：\n")
		limit := len(cands)
		if limit > 5 {
			limit = 5
		}
		for i := 1; i < limit; i++ {
			b.WriteString(fmt.Sprintf("%d) %s\n   url: %s\n", i, cands[i].Source, cands[i].URL))
		}
		return b.String(), nil
	}

	return "", fmt.Errorf("没找到可用的图片（关键词 %q）。尝试记录：%s。换更通用的英文关键词，最多再试 2 次",
		keyword, strings.Join(notes, " | "))
}

func (t *ImageTool) download(ctx context.Context, rawURL, relPath, full, source string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("不是合法的 URL：%q", rawURL)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("只允许 https，收到 %q", u.Scheme)
	}
	if !hostAllowed(u.String(), t.hosts) {
		return "", fmt.Errorf("域名 %q 不在白名单内", u.Hostname())
	}

	data, ctype, err := netutil.Fetch(ctx, u.String(), imageMaxBytes)
	if err != nil {
		return "", err
	}
	if ctype == "image/svg+xml" {
		return "", fmt.Errorf("拿到的是 SVG，飞书不支持发送。请先转 PNG：sips -s format png in.svg --out %s", relPath)
	}
	if !strings.HasPrefix(ctype, "image/") {
		return "", fmt.Errorf("下载到的不是图片（内容类型 %s）", ctype)
	}

	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败：%w", err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", fmt.Errorf("保存图片失败：%w", err)
	}

	// 把图片塞进 carrier，engine 会用它作为一条 user 消息回传给模型
	if box, ok := ctx.Value(imageCarrierKey{}).(*[]string); ok && box != nil {
		if dataURL, derr := fileToDataURL(full); derr == nil {
			*box = append(*box, dataURL)
		}
	}

	return fmt.Sprintf("已保存到 %s（%s，%d 字节）。来源：%s。", relPath, ctype, len(data), source), nil
}

// ---- 图源 ----

type imageCandidate struct {
	URL     string
	Source  string
	License string
}

func (t *ImageTool) searchPexels(ctx context.Context, keyword string) ([]imageCandidate, error) {
	if t.pexelsKey == "" {
		return nil, nil
	}

	q := url.Values{}
	q.Set("query", keyword)
	q.Set("per_page", "5")
	q.Set("orientation", "landscape")

	req, err := netutil.NewRequest(ctx, "https://api.pexels.com/v1/search?"+q.Encode())
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", t.pexelsKey)

	data, err := netutil.DoJSON(req, 2<<20)
	if err != nil {
		return nil, fmt.Errorf("Pexels 搜索失败：%w", err)
	}

	var resp struct {
		Photos []struct {
			Alt string `json:"alt"`
			Src struct {
				Large  string `json:"large"`
				Medium string `json:"medium"`
			} `json:"src"`
		} `json:"photos"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("解析 Pexels 响应失败：%w", err)
	}

	var out []imageCandidate
	for _, p := range resp.Photos {
		u := p.Src.Large
		if u == "" {
			u = p.Src.Medium
		}
		if u == "" {
			continue
		}
		out = append(out, imageCandidate{URL: u, Source: "Pexels（" + p.Alt + "）", License: "Pexels License"})
	}
	return out, nil
}

func searchCommons(ctx context.Context, keyword string) ([]imageCandidate, error) {
	q := url.Values{}
	q.Set("action", "query")
	q.Set("generator", "search")
	q.Set("gsrsearch", "filetype:bitmap "+keyword)
	q.Set("gsrnamespace", "6")
	q.Set("gsrlimit", "6")
	q.Set("prop", "imageinfo")
	q.Set("iiprop", "url|mime|size")
	q.Set("iiurlwidth", "800")
	q.Set("format", "json")

	data, _, err := netutil.Fetch(ctx, "https://commons.wikimedia.org/w/api.php?"+q.Encode(), 2<<20)
	if err != nil {
		return nil, fmt.Errorf("Wikimedia 搜索失败：%w", err)
	}

	var resp struct {
		Query struct {
			Pages map[string]struct {
				Title     string `json:"title"`
				ImageInfo []struct {
					Mime     string `json:"mime"`
					ThumbURL string `json:"thumburl"`
					URL      string `json:"url"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("解析 Wikimedia 响应失败：%w", err)
	}

	// map 顺序随机，排序让同样关键词结果稳定
	keys := make([]string, 0, len(resp.Query.Pages))
	for k := range resp.Query.Pages {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []imageCandidate
	for _, k := range keys {
		p := resp.Query.Pages[k]
		if len(p.ImageInfo) == 0 {
			continue
		}
		ii := p.ImageInfo[0]
		switch ii.Mime {
		case "image/jpeg", "image/png", "image/webp":
		default:
			continue
		}
		u := ii.ThumbURL
		if u == "" {
			u = ii.URL
		}
		if u == "" {
			continue
		}
		out = append(out, imageCandidate{
			URL:     u,
			Source:  "Wikimedia Commons（" + strings.TrimPrefix(p.Title, "File:") + "）",
			License: "CC，详见文件页",
		})
	}
	return out, nil
}

func searchOpenverse(ctx context.Context, keyword string) ([]imageCandidate, error) {
	q := url.Values{}
	q.Set("q", keyword)
	q.Set("page_size", "6")
	q.Set("extension", "jpg")

	data, _, err := netutil.Fetch(ctx, "https://api.openverse.org/v1/images/?"+q.Encode(), 2<<20)
	if err != nil {
		return nil, fmt.Errorf("Openverse 搜索失败：%w", err)
	}

	var resp struct {
		Results []struct {
			Title    string `json:"title"`
			URL      string `json:"url"`
			Creator  string `json:"creator"`
			License  string `json:"license"`
			Provider string `json:"provider"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("解析 Openverse 响应失败：%w", err)
	}

	var out []imageCandidate
	for _, r := range resp.Results {
		if r.URL == "" || !hostAllowed(r.URL, imageHosts) {
			continue
		}
		out = append(out, imageCandidate{
			URL:     r.URL,
			Source:  fmt.Sprintf("Openverse/%s（%s，作者 %s）", r.Provider, r.Title, r.Creator),
			License: r.License,
		})
	}
	return out, nil
}

func hostAllowed(rawURL string, hosts []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return false
	}
	h := u.Hostname()
	for _, allowed := range hosts {
		if h == allowed || strings.HasSuffix(h, "."+allowed) {
			return true
		}
	}
	return false
}

// ---- 图片回传给模型 ----

type imageCarrierKey struct{}

func withImageCarrier(ctx context.Context) (context.Context, *[]string) {
	box := &[]string{}
	return context.WithValue(ctx, imageCarrierKey{}, box), box
}

func imageCarrierFrom(ctx context.Context) []string {
	if box, ok := ctx.Value(imageCarrierKey{}).(*[]string); ok && box != nil {
		return *box
	}
	return nil
}

func fileToDataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读图片失败：%w", err)
	}
	if len(data) > 32<<20 {
		return "", fmt.Errorf("图片太大（%d 字节）", len(data))
	}
	ctype := http.DetectContentType(data)
	switch ctype {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return "", fmt.Errorf("不支持的图片格式 %s", ctype)
	}
	return "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// ---- 二次元图源 ----

// Danbooru：标签最全。rating 由调用方决定（general=纯 SFW，explicit=纯 R18）。
//
// 关键：Danbooru 只认**规范标签**（nahida_(genshin_impact)），不认自由文本。
// 模型天然会写 "nahida genshin_impact r18"，直接拼成 tags 会得到 0 条（HTTP 200 + 空数组），
// 于是它换着关键词重试，越加越多、结果永远为空。所以这里先做一次标签解析。
func searchDanbooru(ctx context.Context, keyword, rating string) ([]imageCandidate, error) {
	ratingTag := "rating:" + rating
	tags := danbooruTags(ctx, keyword)

	q := url.Values{}
	q.Set("tags", ratingTag+" "+tags)
	q.Set("limit", "8")

	data, _, err := netutil.Fetch(ctx, "https://danbooru.donmai.us/posts.json?"+q.Encode(), 4<<20)
	if err != nil {
		// 422 多半是 tag 太多（匿名上限 2 个）→ 只留第一个再试一次
		fields := strings.Fields(tags)
		if len(fields) > 1 {
			q.Set("tags", ratingTag+" "+fields[0])
			if d2, _, err2 := netutil.Fetch(ctx, "https://danbooru.donmai.us/posts.json?"+q.Encode(), 4<<20); err2 == nil {
				data, err = d2, nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("Danbooru 搜索失败：%w", err)
		}
	}

	posts, err := parseDanbooruPosts(data)
	if err != nil {
		return nil, err
	}

	// 一条都没有：把标签拆开，只用最"重"的那个再试一次。
	// 原因是模型经常叠加多个条件（角色+发色+服装），交集直接为空。
	if len(posts) == 0 {
		first := strings.Fields(tags)
		if len(first) > 1 {
			q.Set("tags", ratingTag+" "+first[0])
			if data, _, err := netutil.Fetch(ctx, "https://danbooru.donmai.us/posts.json?"+q.Encode(), 4<<20); err == nil {
				posts, _ = parseDanbooruPosts(data)
			}
		}
	}
	if len(posts) == 0 {
		return nil, fmt.Errorf("Danbooru 没有同时匹配「%s」的图（rating=%s）。"+
			"换更常见的标签，比如只给角色名或只给一个特征", tags, rating)
	}

	var out []imageCandidate
	for _, p := range posts {
		u := p.SampleFileURL
		if u == "" {
			u = p.LargeFileURL
		}
		if u == "" {
			u = p.FileURL
		}
		if u == "" || !strings.HasPrefix(u, "https://") {
			continue
		}
		src := "Danbooru"
		if p.TagString != "" {
			t := strings.ReplaceAll(p.TagString, " ", "_")
			if len(t) > 60 {
				t = t[:60] + "…"
			}
			src += "（" + t + "）"
		}
		out = append(out, imageCandidate{URL: u, Source: src + "｜实际标签：" + tags, License: "同人图，请勿商用"})
	}
	return out, nil
}

type danbooruPost struct {
	LargeFileURL  string `json:"large_file_url"`
	SampleFileURL string `json:"sample_file_url"`
	FileURL       string `json:"file_url"`
	TagString     string `json:"tag_string_character"`
}

func parseDanbooruPosts(data []byte) ([]danbooruPost, error) {
	var posts []danbooruPost
	if err := json.Unmarshal(data, &posts); err != nil {
		return nil, fmt.Errorf("解析 Danbooru 响应失败：%w", err)
	}
	return posts, nil
}

// danbooruTags 把自由文本（"nahida genshin_impact r18"）解析成规范标签。
// 做法：每个词去 /tags.json 搜前缀，按 post_count 挑最热门的那个；
// 明确限制级的关键词直接丢掉——本工具只出 SFW。
func danbooruTags(ctx context.Context, keyword string) string {
	skip := map[string]bool{
		"r18": true, "nsfw": true, "explicit": true, "rating": true,
		"hentai": true, "lewd": true, "sexy": true,
	}

	words := strings.Fields(strings.ToLower(keyword))
	var picked []string
	for _, w := range words[:min(len(words), 4)] {
		if skip[w] || len([]rune(w)) < 3 {
			continue
		}
		if tag := bestDanbooruTag(ctx, w); tag != "" {
			picked = append(picked, tag)
		}
	}
	if len(picked) == 0 {
		return strings.ReplaceAll(strings.TrimSpace(keyword), " ", "_")
	}
	// Danbooru 匿名搜索最多 2 个 tag，第 3 个直接 422。
	// 保留第一个（通常是角色名，最具体）+ 热度最高的那个。
	if len(picked) > 2 {
		picked = []string{picked[0], picked[len(picked)-1]}
	}
	return strings.Join(picked, " ")
}

// bestDanbooruTag 在**所有分类**里找前缀匹配，取 post_count 最高的。
//
// 不要限定 category=4（角色）：实测 "green_hair" 会匹配到一个冷门角色
// green_haired_cure_(...)，而真正的通用标签 green_hair 有 60 万张。
// 让"热度"自己说话就够了：
//
//	nahida         → nahida_(genshin_impact)  13642
//	green_hair     → green_hair              607162
//	genshin_impact → genshin_impact          321654
func bestDanbooruTag(ctx context.Context, word string) string {
	q := url.Values{}
	q.Set("search[name_matches]", word+"*")
	q.Set("search[order]", "count")
	q.Set("limit", "5")

	data, _, err := netutil.Fetch(ctx, "https://danbooru.donmai.us/tags.json?"+q.Encode(), 1<<20)
	if err != nil {
		return ""
	}

	var tags []struct {
		Name      string `json:"name"`
		PostCount int    `json:"post_count"`
	}
	if err := json.Unmarshal(data, &tags); err != nil {
		return ""
	}

	best, bestCount := "", 0
	for _, t := range tags {
		if t.PostCount > bestCount {
			best, bestCount = t.Name, t.PostCount
		}
	}
	return best
}

// Safebooru：全站 SFW，无需 rating 过滤
func searchSafebooru(ctx context.Context, keyword string) ([]imageCandidate, error) {
	tags := strings.ReplaceAll(strings.TrimSpace(keyword), " ", "_")
	q := url.Values{}
	q.Set("page", "dapi")
	q.Set("s", "post")
	q.Set("q", "index")
	q.Set("tags", tags)
	q.Set("limit", "8")
	q.Set("json", "1")

	data, _, err := netutil.Fetch(ctx, "https://safebooru.org/index.php?"+q.Encode(), 4<<20)
	if err != nil {
		return nil, fmt.Errorf("Safebooru 搜索失败：%w", err)
	}

	var posts []struct {
		Directory int    `json:"directory"` // 注意：Safebooru 返回的是数字
		Image     string `json:"image"`
	}
	if err := json.Unmarshal(data, &posts); err != nil {
		return nil, fmt.Errorf("解析 Safebooru 响应失败：%w", err)
	}

	var out []imageCandidate
	for _, p := range posts {
		if p.Image == "" {
			continue
		}
		out = append(out, imageCandidate{
			URL:     fmt.Sprintf("https://safebooru.org/images/%d/%s", p.Directory, p.Image),
			Source:  "Safebooru",
			License: "同人图，请勿商用",
		})
	}
	return out, nil
}

// nekos.best：随机二次元图，忽略关键词，只作为最后兜底
func searchNekosBest(ctx context.Context, keyword string) ([]imageCandidate, error) {
	data, _, err := netutil.Fetch(ctx, "https://nekos.best/api/v2/neko", 1<<20)
	if err != nil {
		return nil, fmt.Errorf("nekos.best 失败：%w", err)
	}
	var resp struct {
		Results []struct {
			URL    string `json:"url"`
			Artist string `json:"artist_name"`
			Source string `json:"source_url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("解析 nekos.best 响应失败：%w", err)
	}
	var out []imageCandidate
	for _, r := range resp.Results {
		if r.URL == "" {
			continue
		}
		out = append(out, imageCandidate{
			URL:     r.URL,
			Source:  "nekos.best（随机，作者 " + r.Artist + "）",
			License: "同人图，请勿商用",
		})
	}
	return out, nil
}
