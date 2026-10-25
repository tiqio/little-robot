package tools

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"little-robot/internal/netutil"
	"little-robot/internal/schema"
	"little-robot/internal/sources"
)

/*
fetch_hentai：从 nhentai（最大的免费本子库）抓本子。

两种用法：
- id_or_url：本子的数字或链接（12345 / 123-abc / https://nhentai.net/g/12345/）
  抓封面 + 整本，存到工作区，封面回传给模型"看"。
- query：搜索模式，返回前 5 个候选（前 3 个封面会回传给模型看），
  挑定后带 id_or_url 再调一次。

format=zip（默认）：整本打包成一个 zip 文件（优先官方下载接口，没 key 就
逐页下载自己用标准库打 zip）→ 最后用 send 一次发给用户；
format=images：逐页存成 001.jpg... → 逐张 send（>30 页建议 zip）。
*/

var _ BaseTool = &FetchHentaiTool{}

type FetchHentaiTool struct {
	workDir string
	cli     *sources.Nhentai
}

func NewFetchHentaiTool(workDir string, cli *sources.Nhentai) *FetchHentaiTool {
	return &FetchHentaiTool{workDir: workDir, cli: cli}
}

func (t *FetchHentaiTool) Name() string { return "fetch_hentai" }

func (t *FetchHentaiTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: `从 nhentai 抓一本二次元本子（用户说"本子/同人本/nh/抓 g/12345"都用这个）。

用法（三选一，优先级 id_or_url > query > page）：
A. id_or_url：本子数字或链接（12345 / 123-abc / https://nhentai.net/g/12345/）。
   抓回封面 + 整本，封面会附在返回里，先看一眼是不是用户要的那本。
B. query：搜索模式（"sonohara"、"artist:xxx language:english"、"tag:\"big breasts\" pages:>10"）。
   返回前 5 个候选（前 3 个封面已附上），挑一个再用 id_or_url 调一次。
C. page：最新本子列表的第 N 页（对应官网 https://nhentai.net/?page=N，第 1 页 = 最新）。
   用户说"看看今天/最近有什么新本子"就用它，page 不填就是第 1 页。

format（仅 A 用，默认 zip）：
- zip：整本一个 zip 文件 → send(封面) + send(zip)，一次发齐，推荐；
- images：逐页 001.jpg... → 逐张 send（页数多时很刷屏，>30 页建议 zip）。

发完补一句总结：标题 / 作者 / 页数 / 来源链接（https://nhentai.net/g/<id>/）。`,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id_or_url": map[string]interface{}{
					"type":        "string",
					"description": "本子数字或 nhentai 链接，例如 12345、123-abc、https://nhentai.net/g/12345/；与 query/page 三选一",
				},
				"query": map[string]interface{}{
					"type":        "string",
					"description": "搜索词，例如 sonohara 或 artist:xxx language:english；与 id_or_url/page 二选一",
				},
				"page": map[string]interface{}{
					"type":        "integer",
					"description": "最新本子列表页码（官网 ?page=N 的 N），1=最新；只填 page 不填其他就是翻最新列表",
				},
				"format": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"zip", "images"},
					"description": "zip=整本一个文件（默认）；images=逐页图片",
				},
			},
		},
	}
}

func (t *FetchHentaiTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		IDOrURL string `json:"id_or_url"`
		Query   string `json:"query"`
		Page    int    `json:"page"`
		Format  string `json:"format"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%w", err)
	}
	if strings.TrimSpace(in.IDOrURL) == "" && strings.TrimSpace(in.Query) == "" && in.Page < 1 {
		return "", fmt.Errorf("id_or_url / query / page 至少给一个")
	}
	format := strings.ToLower(strings.TrimSpace(in.Format))
	if format == "" {
		format = "zip"
	}
	switch format {
	case "zip", "images":
	default:
		return "", fmt.Errorf("format 只能是 zip 或 images，收到 %q", in.Format)
	}

	dir := effectiveWorkDir(ctx, t.workDir)

	// B. 列表模式：搜索 / 最新列表
	if strings.TrimSpace(in.IDOrURL) == "" {
		if strings.TrimSpace(in.Query) != "" {
			return t.searchMode(ctx, dir, in.Query)
		}
		return t.latestMode(ctx, dir, in.Page)
	}

	// A. 详情模式
	id, err := sources.ParseGalleryID(in.IDOrURL)
	if err != nil {
		return "", err
	}
	g, err := t.cli.Gallery(ctx, id)
	if err != nil {
		return "", err
	}

	base := filepath.Join("out", "hentai", strconv.Itoa(g.ID))
	if err := os.MkdirAll(filepath.Join(dir, base), 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败：%w", err)
	}

	// 封面：先下载并回传给模型"看"。封面只是预览，挂了不挡整本书：
	// 第 1 页通常就是封面/扉页，拿它顶一下。
	var coverNote string
	coverRel := filepath.Join(base, "cover.jpg")
	hasCover := false
	coverData, err := t.cli.FetchCover(ctx, g)
	if err != nil && len(g.Pages) > 0 {
		coverData, err = t.cli.FetchPage(ctx, g.Pages[0])
		coverNote = "（封面 CDN 挂了，用第 1 页代替预览）"
	}
	if coverData != nil {
		if werr := os.WriteFile(filepath.Join(dir, coverRel), coverData, 0o644); werr == nil {
			hasCover = true
			attachImage(ctx, filepath.Join(dir, coverRel)) // 封面回传给模型"看着挑"
		}
	} else {
		coverNote = "（封面和第 1 页都没抓到，不影响整本）"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("抓到本子 %d：%s\n", g.ID, g.EnglishTitle))
	if g.JapaneseTitle != "" && g.JapaneseTitle != g.EnglishTitle {
		b.WriteString("原题：" + g.JapaneseTitle + "\n")
	}
	if artists := tagNames(g.Tags, "artist"); artists != "" {
		b.WriteString("作者：" + artists + "\n")
	}
	if chars := tagNames(g.Tags, "character"); chars != "" {
		b.WriteString("角色：" + chars + "\n")
	}
	b.WriteString(fmt.Sprintf("页数：%d｜收藏：%d｜上传：%s%s\n", g.NumPages, g.NumFavorites, g.UploadDate.Format("2006-01-02"), coverNote))

	coverHint := ""
	if hasCover {
		coverHint = "send(" + coverRel + ") 发封面，再"
	}

	switch format {
	case "images":
		paths, perr := t.downloadAllPages(ctx, dir, base, g)
		if perr != nil {
			b.WriteString("⚠️ 部分页失败：" + perr.Error() + "\n")
		}
		b.WriteString(fmt.Sprintf("页面已存到 %s/001.jpg…（共 %d 页）\n", base, len(paths)))
		b.WriteString(coverHint + "逐张 send 页面；页多（>30）就告诉用户下次用 zip")
	default: // zip
		zipRel, zerr := t.makeZip(ctx, dir, base, g)
		if zerr != nil {
			return "", zerr
		}
		b.WriteString("整本已打包：" + zipRel + "\n")
		b.WriteString(coverHint + "send(" + zipRel + ") 发整本")
	}
	return b.String(), nil
}

// searchMode 搜索：候选列表 + 前 3 个封面回传给模型看
func (t *FetchHentaiTool) searchMode(ctx context.Context, dir, query string) (string, error) {
	items, total, err := t.cli.Search(ctx, query)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return fmt.Sprintf("nhentai 搜索 %q 共 0 条结果。换更短的关键词试试，或换 artist:xxx 形式", query), nil
	}
	return t.listMode(ctx, dir, fmt.Sprintf("nhentai 搜索 %q", query), items, total)
}

// latestMode 最新本子列表：官网 ?page=N 的 JSON 版（按上传时间倒序）
func (t *FetchHentaiTool) latestMode(ctx context.Context, dir string, page int) (string, error) {
	if page < 1 {
		page = 1
	}
	items, total, err := t.cli.Latest(ctx, page, 25)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return fmt.Sprintf("nhentai 最新列表第 %d 页是空的（翻太深了？）", page), nil
	}
	return t.listMode(ctx, dir, fmt.Sprintf("nhentai 最新列表 第 %d 页（新→旧）", page), items, total)
}

// listMode 渲染一批候选 + 把前 3 个封面下载到工作区回传给模型"看着挑"
func (t *FetchHentaiTool) listMode(ctx context.Context, dir, heading string, items []sources.GallerySummary, total int) (string, error) {
	var b strings.Builder
	n := len(items)
	if n > 8 {
		n = 8
	}
	b.WriteString(heading + fmt.Sprintf("：共 %d 条，前 %d 个：\n", total, n))
	for i, it := range items {
		if i >= n {
			break
		}
		title := it.EnglishTitle
		if title == "" {
			title = it.JapaneseTitle
		}
		b.WriteString(fmt.Sprintf("%d. id=%d 《%s》 %d页 ♥%d\n", i+1, it.ID, title, it.NumPages, it.NumFavorites))
	}

	// 前 3 个封面下载到工作区并回传给模型，让它"看着封面挑"
	for i, it := range items {
		if i >= 3 {
			break
		}
		data, err := t.cli.FetchThumb(ctx, it.ThumbPath)
		if err != nil {
			continue
		}
		rel := filepath.Join("out", "hentai", "_search", fmt.Sprintf("thumb_%d.jpg", it.ID))
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, rel), data, 0o644); err != nil {
			continue
		}
		attachImage(ctx, filepath.Join(dir, rel)) // 缩略图回传给模型"看着挑"
	}

	b.WriteString("挑一个最相关的，再调一次并带上 id_or_url=<id>")
	return b.String(), nil
}

// downloadAllPages 4 路并行下载全部页面，返回按页码排好的相对路径
func (t *FetchHentaiTool) downloadAllPages(ctx context.Context, dir, base string, g *sources.Gallery) ([]string, error) {
	paths := make([]string, len(g.Pages))
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	sem := make(chan struct{}, 4)
	for i, p := range g.Pages {
		wg.Add(1)
		go func(i int, p sources.Page) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := t.cli.FetchPage(ctx, p)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			name := fmt.Sprintf("%03d.jpg", p.Number)
			if p.Number > 999 {
				name = fmt.Sprintf("%04d.jpg", p.Number)
			}
			rel := filepath.Join(base, name)
			if werr := os.WriteFile(filepath.Join(dir, rel), data, 0o644); werr != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("保存第 %d 页失败：%w", p.Number, werr))
				mu.Unlock()
				return
			}
			mu.Lock()
			paths[i] = rel
			mu.Unlock()
		}(i, p)
	}
	wg.Wait()
	if len(errs) > 0 {
		return paths, fmt.Errorf("%d 页失败（首条：%v）", len(errs), errs[0])
	}
	return paths, nil
}

// makeZip 整本打包：优先官方下载接口（需要 NHENTAI_API_KEY），失败就逐页下载自己打 zip
func (t *FetchHentaiTool) makeZip(ctx context.Context, dir, base string, g *sources.Gallery) (string, error) {
	// 1) 官方链接
	if t.cli.HasKey() {
		if zurl, _, err := t.cli.DownloadURL(ctx, g.ID, "zip"); err == nil {
			if data, _, err := netutil.FetchWith(ctx, zurl, netutil.FetchOpt{UA: sources.UserAgent, MaxBytes: 40 << 20}); err == nil {
				rel := filepath.Join(base, fmt.Sprintf("%d.zip", g.ID))
				if werr := os.WriteFile(filepath.Join(dir, rel), data, 0o644); werr == nil {
					return rel, nil
				}
			}
		}
		// 官方挂了（429/503/过期）就静默走逐页兜底
	}

	// 2) 逐页下载 + 标准库 archive/zip 自己打包
	paths, perr := t.downloadAllPages(ctx, dir, base, g)
	if perr != nil {
		// 部分页失败也先打 zip，缺页的在返回文本里说
	}
	var files []string
	for _, p := range paths {
		if p != "" {
			files = append(files, filepath.Join(dir, p))
		}
	}
	if len(files) == 0 {
		return "", fmt.Errorf("一页都没下载成功（%v）", perr)
	}
	rel := filepath.Join(base, fmt.Sprintf("%d_pages.zip", g.ID))
	if err := writeZip(filepath.Join(dir, rel), files); err != nil {
		return "", err
	}
	return rel, nil
}

// writeZip 把文件按顺序写进一个 zip
func writeZip(zipPath string, files []string) (err error) {
	out, cerr := os.Create(zipPath)
	if cerr != nil {
		return fmt.Errorf("创建 zip 失败：%w", cerr)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("关闭 zip 失败：%w", cerr)
		}
	}()
	w := zip.NewWriter(out)
	for _, f := range files {
		data, rerr := os.ReadFile(f)
		if rerr != nil {
			return fmt.Errorf("读页面失败：%w", rerr)
		}
		fw, aerr := w.Create(filepath.Base(f))
		if aerr != nil {
			return fmt.Errorf("写 zip 条目失败：%w", aerr)
		}
		if _, werr := fw.Write(data); werr != nil {
			return fmt.Errorf("写 zip 内容失败：%w", werr)
		}
	}
	if cerr := w.Close(); cerr != nil && err == nil {
		err = fmt.Errorf("完成 zip 失败：%w", cerr)
	}
	return err
}

// tagNames 把某类标签名拼成一串（截断 120 字符，别把返回撑爆）
func tagNames(tags []sources.Tag, typ string) string {
	var names []string
	for _, tg := range tags {
		if tg.Type == typ {
			names = append(names, tg.Name)
		}
	}
	s := strings.Join(names, ", ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
