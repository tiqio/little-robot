package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"little-robot/internal/netutil"
	"little-robot/internal/pdf"
	"little-robot/internal/schema"
	"little-robot/internal/sources"
)

/*
fetch_hentai：从 nhentai（最大的免费本子库）抓本子。

两种用法：
- id_or_url：本子的数字或链接（12345 / 123-abc / https://nhentai.net/g/12345/）
  抓封面 + 整本，存到工作区，封面回传给模型"看"。
- query：搜索模式，返回前 5 个候选（标题+作者+页数，封面存盘可用 view_image 看），
  挑定后带 id_or_url 再调一次。

format=pdf（默认）：整本合成一个 PDF（不加密，手机上直接翻页预览）→ send 一次发齐；
format=zip：整本打成 zip 压缩包（手机需要解压，只在用户明确要压缩包时用）；
format=images：逐页存成 001.jpg... → 逐张 send（很刷屏，>30 页别用）。
*/

var _ BaseTool = &FetchHentaiTool{}

type FetchHentaiTool struct {
	workDir string
	cli     *sources.Nhentai

	// resolveCache 名字解析结果缓存：key 是归一化后的输入。
	// 不加缓存的话，模型连续重试同一个错词会反复打 nhentai（限流 10 次/分钟）。
	resolveMu     sync.Mutex
	cachedResolve map[string]resolveEntry
}

// resolveEntry 一条解析结果：候选名 + 来源说明
type resolveEntry struct {
	names []string
	note  string
}

// cacheResolve 写入缓存（空结果也缓存，避免反复问同一个错名字）
func (t *FetchHentaiTool) cacheResolve(query string, names []string, note string) {
	t.resolveMu.Lock()
	defer t.resolveMu.Unlock()
	if t.cachedResolve == nil {
		t.cachedResolve = make(map[string]resolveEntry)
	}
	if len(t.cachedResolve) > 200 {
		t.cachedResolve = make(map[string]resolveEntry)
	}
	t.cachedResolve[normalizeAlias(query)] = resolveEntry{names: names, note: note}
}

func NewFetchHentaiTool(workDir string, cli *sources.Nhentai) *FetchHentaiTool {
	return &FetchHentaiTool{workDir: workDir, cli: cli}
}

func (t *FetchHentaiTool) Name() string { return "fetch_hentai" }

func (t *FetchHentaiTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: `从 nhentai 抓一本二次元本子（用户说"本子/同人本/nh/抓 g/12345"，或贴了 nhentai.net 链接，都用这个）。

适用：用户要**整本**（多页、要打包/要收藏）。
用户只要**一张图**（壁纸/角色图/色图）→ 用 fetch_image(source=anime)，别为一张图抓整本。

用法（三选一，优先级 id_or_url > query > page）：
A. id_or_url：本子数字或链接（12345 / 123-abc / https://nhentai.net/g/12345/）。
   抓回封面 + 整本，封面会附在返回里，先看一眼是不是用户要的那本。
B. query：搜索模式（"sonohara"、"artist:xxx language:english"、"tag:\"big breasts\" pages:>10"）。
   返回前 5 个候选，挑一个再用 id_or_url 调一次。
   候选不含图片（省上下文）：要核对封面就 view_image(out/hentai/_search/thumb_<id>.jpg)。
C. page：最新本子列表的第 N 页（对应官网 https://nhentai.net/?page=N，第 1 页 = 最新）。
   用户说"看看今天/最近有什么新本子"就用它，page 不填就是第 1 页。

format（仅 A 用，默认 pdf）：
- pdf：整本合成一个 PDF → send(封面) + send(pdf)，一次发齐，**推荐**：
  不加密、手机上直接翻页，比 zip 好用得多；
- zip：整本打成一个 zip 压缩包 → 手机要先解压，只有用户明确说"要压缩包/zip"才用；
- images：逐页 001.jpg... → 逐张 send（很刷屏，>30 页别用）。
整本太大时会自动拆成 xxx-part1.pdf、xxx-part2.pdf，就依次 send 每一份。

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
					"enum":        []string{"pdf", "zip", "images"},
					"description": "pdf=整本一个 PDF（默认，手机可直接预览）；zip=压缩包（手机要解压）；images=逐页图片",
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
		format = "pdf"
	}
	switch format {
	case "pdf", "zip", "images":
	default:
		return "", fmt.Errorf("format 只能是 pdf / zip / images，收到 %q", in.Format)
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
			// 注意：封面**不**进模型上下文。
			// 一张封面的 data URL ≈ 10 万字符，进了历史就会每轮都被重新喂一遍，
			// 结果是"每轮都超阈值、每轮都压缩、模型每轮失忆"（线上实测就是这样）。
			// 判断对不对味靠标题/作者/角色标签就够了；真要核对，模型可以用 view_image。
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
		b.WriteString(coverHint + "逐张 send 页面；页多（>30）就告诉用户下次用 pdf")
	case "zip":
		zipRel, zerr := t.makeZip(ctx, dir, base, g)
		if zerr != nil {
			return "", zerr
		}
		b.WriteString("整本已打包：" + zipRel + "\n")
		b.WriteString(coverHint + "send(" + zipRel + ") 发整本")
	default: // pdf：手机上直接翻，不用解压
		pdfs, perr := t.makePDF(ctx, dir, base, g)
		if perr != nil {
			return "", perr
		}
		if len(pdfs) == 1 {
			b.WriteString("整本已合成 PDF：" + pdfs[0] + "\n")
			b.WriteString(coverHint + fmt.Sprintf("send(%s, name=\"%s（%d页）.pdf\") 发整本",
				pdfs[0], truncateTitle(g.EnglishTitle), g.NumPages))
		} else {
			b.WriteString(fmt.Sprintf("整本较大，已拆成 %d 个 PDF：\n", len(pdfs)))
			for i, p := range pdfs {
				b.WriteString(fmt.Sprintf("  %d) %s\n", i+1, p))
			}
			b.WriteString(coverHint + "按顺序 send 每一个 PDF。\n")
			b.WriteString("name 这么写（必须带 .pdf）：\n")
			for i, p := range pdfs {
				b.WriteString(fmt.Sprintf("  send(%s, name=\"%s 第%d部分（共%d部分）.pdf\")\n",
					p, truncateTitle(g.EnglishTitle), i+1, len(pdfs)))
			}
		}
	}
	return b.String(), nil
}

// searchMode 搜索：候选列表 + 前 3 个封面回传给模型看
func (t *FetchHentaiTool) searchMode(ctx context.Context, dir, query string) (string, error) {
	// 先用原词搜；0 条时自动降级再试几种写法。
	// 真实事故：模型用 "aysa wuthering_waves chinese" 连搜 80 次全 0 条——
	// nhentai 的 query 是**多词 AND**，多一个无义词（chinese）就从有结果变 0 条，
	// 而模型只会换同义词重试，永远撞同一堵墙。工具自己降级比让模型猜划算得多。
	// 0 条时先做"名字解析"：本地别名表 + 作品反查角色标签。
	// 这一步才是解决"短语没效果"的关键——模型猜的英文名可能根本不存在，
	// 换多少种写法都没用，必须换成站点上真实存在的标签。
	derived, note := t.resolveNames(ctx, dir, query)
	for _, name := range derived {
		items, total, err := t.cli.Search(ctx, name)
		if err != nil {
			return "", err
		}
		if len(items) > 0 {
			return t.listMode(ctx, dir, fmt.Sprintf("nhentai 搜索 %q（%s）", name, note), items, total)
		}
	}

	// 限流是 10 次/分钟，降级链最多试 4 个词：再多就为了一个搜不到的名字把配额烧光
	tries := searchVariants(query)
	if len(tries) > maxSearchTries {
		tries = tries[:maxSearchTries]
	}
	var tried []string
	for _, q := range tries {
		items, total, err := t.cli.Search(ctx, q)
		if err != nil {
			// 429/503 这类限流不算"没结果"，直接把错误交给模型，它才知道该等
			return "", err
		}
		tried = append(tried, q)
		if len(items) == 0 {
			continue
		}
		label := fmt.Sprintf("nhentai 搜索 %q", q)
		if q != tries[0] {
			label += fmt.Sprintf("（原词 %q 是 0 条，自动降级后命中）", tries[0])
		}
		return t.listMode(ctx, dir, label, items, total)
	}
	return zeroResultHint(query, tried), nil
}

// maxSearchTries 一次搜索最多试几种写法（限流 10 次/分钟，别为了一个错名字烧光配额）
const maxSearchTries = 4

// searchVariants 生成"同一意图的几种写法"，从最具体到最宽松：
//  1. 原样
//  2. 去掉语言词（chinese/english/japanese/中文…）——它们会让 AND 搜索变 0 条
//  3. 只要第一个词（通常是角色名，最具体也最可能命中）
//  4. 下划线换成空格（nhentai 不吃 danbooru 风格的下划线标签）
//  5. 逐个词单独试（长的优先，下划线词组整体转空格）——角色名错、作品名对时还能救回来
func searchVariants(query string) []string {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	out := []string{q}
	seen := map[string]bool{q: true}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}

	drop := map[string]bool{
		"chinese": true, "english": true, "japanese": true, "korean": true,
		"chinese,": true, "中文": true, "汉化": true, "漢化": true, "translated": true,
		"language": true, "translation": true,
	}
	var kept []string
	for _, w := range strings.Fields(q) {
		if !drop[strings.ToLower(w)] {
			kept = append(kept, w)
		}
	}
	if len(kept) > 0 {
		add(strings.Join(kept, " "))
	}
	if len(kept) > 1 {
		add(kept[0])
	}
	if strings.Contains(q, "_") {
		add(strings.ReplaceAll(q, "_", " "))
	}
	// 逐个"词"单独试（长的优先）：角色名写错的时候，作品名往往是对的。
	// 注意保留下划线词组的整体性：wuthering_waves 要变成 "wuthering waves"
	// 而不是拆成两个无义词（真实数据：wuthering_waves=0 条，wuthering waves=25 条）。
	//
	// 泛词必须排除：单独搜 "chinese" 会命中一大票无关本子，
	// 还顺带挂上封面图，把上下文直接撑爆（线上就是这样涨到 55 万字符的）。
	tokens := strings.Fields(q)
	sort.SliceStable(tokens, func(i, j int) bool { return len(tokens[i]) > len(tokens[j]) })
	for i, tok := range tokens {
		if i >= 3 {
			break
		}
		if drop[strings.ToLower(tok)] || len([]rune(tok)) < 3 {
			continue
		}
		add(strings.ReplaceAll(tok, "_", " "))
	}
	return out
}

// zeroResultHint 全都 0 条时，给模型可执行的下一步，而不是一句"换更短的关键词"
func zeroResultHint(query string, tried []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "nhentai 搜 %q 没有结果。已自动试过：%s\n", query, strings.Join(tried, " / "))
	b.WriteString("这个关键词大概率不存在（常见原因：角色名写成了中文音译）。下一步选一个：\n")
	b.WriteString("1) 换成角色/作品的**英文名**再搜，例如 aemeath（爱弥斯）、illyasviel（伊莉雅）；\n")
	b.WriteString("2) 只留一个词，不要加 chinese/english 这类语言词（多词是 AND，越多越可能 0 条）；\n")
	b.WriteString("3) 用 artist:作者名 或 tag:标签 搜；\n")
	b.WriteString("4) 直接搜作品名（如 wuthering waves）看最新投稿，或让用户提供本子编号/链接。")
	return b.String()
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

// listMode 渲染一批候选 + 把**第 1 个**封面回传给模型"看着挑"。
//
// 为什么只挂 1 张：缩略图的 data URL 一张就是十几万字符（≈3~4 万 token），
// 挂 3 张等于每轮往历史里塞 30 万字符。实测就是这样把上下文顶到 55 万、
// 预填充要走十几分钟的。想看别的封面，模型可以再调一次。
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

	// 缩略图只存盘、不进上下文：data URL 一张 ≈ 10 万字符，
	// 每轮都喂给模型足以把历史撑爆，并触发"每轮压缩 → 每轮失忆"的恶性循环。
	// 文本里给路径，需要看再用 view_image；用户端则有 send 发真图。
	if len(items) > 0 {
		it := items[0]
		if data, terr := t.cli.FetchThumb(ctx, it.ThumbPath); terr == nil {
			rel := filepath.Join("out", "hentai", "_search", fmt.Sprintf("thumb_%d.jpg", it.ID))
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err == nil {
				if err := os.WriteFile(filepath.Join(dir, rel), data, 0o644); err == nil {
					b.WriteString(fmt.Sprintf("（第 1 个候选的封面已存盘：%s；要核对可 view_image）\n", rel))
				}
			}
		}
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

// makePDF 整本做成 PDF：手机上直接翻，不用解压。
//
// 两条取材路径：
//  1. 官方 zip 一条请求拿全本（需要 NHENTAI_API_KEY）→ 内存里解压，省 N 次抓取、也少撞限流；
//  2. 没有 key 或官方挂了 → 逐页下载，再用这些页拼 PDF。
//
// 超过飞书单文件上限就按页拆成 part1/part2…，让模型依次发（不加密，手机能直接预览）。
func (t *FetchHentaiTool) makePDF(ctx context.Context, dir, base string, g *sources.Gallery) ([]string, error) {
	var images [][]byte

	if t.cli.HasKey() {
		if zurl, _, err := t.cli.DownloadURL(ctx, g.ID, "zip"); err == nil {
			// 官方包是整本原图，上百页也就几十 MB；给到 150MB 足够
			if data, _, err := netutil.FetchWith(ctx, zurl, netutil.FetchOpt{UA: sources.UserAgent, MaxBytes: 150 << 20}); err == nil {
				if imgs, zerr := zipPageImages(data); zerr == nil && len(imgs) > 0 {
					images = imgs
					log.Printf("[hentai] %d 用官方包拼 PDF（%d 页）", g.ID, len(imgs))
				}
			}
		}
	}

	if len(images) == 0 {
		paths, perr := t.downloadAllPages(ctx, dir, base, g)
		if perr != nil {
			log.Printf("[hentai] %d 部分页下载失败：%v", g.ID, perr)
		}
		for _, p := range paths {
			if p == "" {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(dir, p))
			if rerr != nil {
				continue
			}
			images = append(images, data)
		}
		if len(images) == 0 {
			return nil, fmt.Errorf("一页都没下载成功（%v）", perr)
		}
	}

	absBase := filepath.Join(dir, base)
	parts, err := writePDFParts(absBase, strconv.Itoa(g.ID), images, pdfPartLimit)
	if err != nil {
		return nil, err
	}
	// 转成相对路径返回给模型（send 要的是工作区相对路径）
	rels := make([]string, len(parts))
	for i, p := range parts {
		r, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil, fmt.Errorf("计算相对路径失败：%w", rerr)
		}
		rels[i] = r
	}
	return rels, nil
}

// pdfPartLimit 单个 PDF 的体积上限：飞书文件 30MB，留点余量
const pdfPartLimit = 25 << 20

// writePDFParts 把图片拼成 PDF；超限就按页二分拆成多个 part。
// 返回生成的文件绝对路径（1 个或 N 个）。
func writePDFParts(dir, name string, images [][]byte, limit int64) ([]string, error) {
	whole := filepath.Join(dir, name+".pdf")
	if err := pdf.BuildImagesFromMemory(whole, images); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(whole); err == nil && fi.Size() <= limit {
		return []string{whole}, nil
	}
	if len(images) == 1 {
		return nil, fmt.Errorf("单页就超过 %dMB，没法拆（可以改用 format=images）", limit>>20)
	}

	// 超限：删掉整本，按页对半拆（递归，直到每份都装得下）
	if err := os.Remove(whole); err != nil {
		return nil, fmt.Errorf("删除超限 PDF 失败：%w", err)
	}
	mid := len(images) / 2
	var out []string
	for i, chunk := range [][]([]byte){images[:mid], images[mid:]} {
		sub, err := writePDFParts(dir, fmt.Sprintf("%s-part%d", name, i+1), chunk, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// zipPageImages 从官方 zip 里按文件名顺序取出所有图片页。
// 官方包结构是 <id>/001.jpg…，按名字排序就是页序。
func zipPageImages(data []byte) ([][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("解析 zip 失败：%w", err)
	}
	files := make([]*zip.File, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(f.Name)) {
		case ".jpg", ".jpeg", ".png", ".webp", ".gif":
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	out := make([][]byte, 0, len(files))
	for _, f := range files {
		rc, oerr := f.Open()
		if oerr != nil {
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(rc, 32<<20))
		rc.Close()
		if rerr != nil || len(b) == 0 {
			continue
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("zip 里没有图片")
	}
	return out, nil
}

// writeZip 把文件按顺序写进一个 zip（format=zip 用）
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

// truncateTitle 标题进文件名前先截断：飞书名字太长会被客户端折行，也挤掉扩展名
func truncateTitle(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

// resolveNames 把"用户说法"翻译成 nhentai 上真实存在的标签。
//
// 两道来源，先便宜后昂贵：
//  1. 本地别名表（离线，零请求）：爱弥斯 → aemeath
//  2. 作品反查：先用作品名搜到本子，再读它的 character 标签
//     （搜 "wuthering waves" 命中后，详情里就有 aemeath 这种真实标签）
//
// 返回候选名 + 一句话来源说明（写进给模型的文本里，它才知道该用哪个词）。
func (t *FetchHentaiTool) resolveNames(ctx context.Context, dir, query string) (names []string, note string) {
	t.resolveMu.Lock()
	if t.cachedResolve != nil {
		if v, ok := t.cachedResolve[normalizeAlias(query)]; ok {
			t.resolveMu.Unlock()
			return v.names, v.note
		}
	}
	t.resolveMu.Unlock()

	// 1) 本地别名
	if aliases := resolveAliases(dir, query); len(aliases) > 0 {
		names, note = aliases, "来自本地别名表，下次直接用这个词搜"
		t.cacheResolve(query, names, note)
		return names, note
	}

	// 2) 作品反查：挑输入里最长的一个词当作品名再搜
	work := longestToken(query)
	if work == "" {
		return nil, ""
	}
	items, _, err := t.cli.Search(ctx, work)
	if err != nil || len(items) == 0 {
		return nil, ""
	}
	// 只看前两个本子的标签，够用且省请求
	seen := map[string]int{}
	var order []string
	for i, it := range items {
		if i >= 2 {
			break
		}
		g, gerr := t.cli.Gallery(ctx, it.ID)
		if gerr != nil {
			continue
		}
		for _, tg := range g.Tags {
			if tg.Type != "character" {
				continue
			}
			if seen[tg.Name] == 0 {
				order = append(order, tg.Name)
			}
			seen[tg.Name]++
		}
	}
	if len(order) == 0 {
		return nil, ""
	}
	// 出现次数多的更可能是用户要的角色
	for i := 0; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			if seen[order[j]] > seen[order[i]] {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	if len(order) > 2 {
		order = order[:2]
	}
	note = fmt.Sprintf("从作品「%s」的本子里反查到角色标签", work)
	t.cacheResolve(query, order, note)
	return order, note
}

// longestToken 取输入里最长的词（通常是作品名/角色名，最可能是对的）
func longestToken(q string) string {
	var best string
	for _, w := range strings.Fields(strings.ReplaceAll(q, "_", " ")) {
		if len([]rune(w)) > len([]rune(best)) {
			best = w
		}
	}
	return best
}
