# 抓图增强方案：爬虫 + 多媒体（图片/PDF）+ 色图图源 + nhentai

> 状态（2026-10-25）：
> - ✅ **M1 文件投递**（ch27）：`send_file` + `FileSender` + `/im/v1/files`，PDF/zip 到飞书客户端
> - ✅ **M2 NSFW 图源**（ch28）：yande.re 主力 + Danbooru `rating` 开关 + `fetch_image` 新增 `rating` 参数
> - ✅ **M4 nhentai**（ch29/ch37）：`fetch_hentai` 工具（id/URL 直抓、query 搜索、page 翻最新列表）+ pdf/zip/images 三种形态（ch37 起默认 PDF，手机直接预览）
> - ✅ **工具归一**（ch30/ch31）：`send_image`+`send_file` 合并为 `send`（按扩展名+大小自动路由消息形态）；描述手术（单图/整本决策规则、看/发流水线、bash 少 curl）
> - ✅ **思考机制重构**（ch32/ch33/ch34）：治"思考吃光额度→静默退出"。S1 finish_reason 透传+空输出救援；S2 思考/行动分家+独立预算；S3 图片降采样+历史图片摘要。详见 `docs/llm-mechanism.md`
> - ✅ **搜索健壮性**（ch44）：零结果自动降级（去语言词/去下划线/逐词）+ 名字解析层（本地别名表 + 作品反查角色标签）+ 打转检测（同参数连续 6 次强制停）
> - ⏳ **M3 通用爬虫** `fetch_page`、**M5 体验增强**（post 网格 / 限流缓存）、**S4 外置查找**（lookup/web_search）待排期
> - ✅ **本子合成 PDF**（ch37）：`internal/pdf` 自研极简 PDF 生成器（每页内嵌 JPEG，不加密），超飞书 30MB 上限自动按页拆分 part1/part2
>
> 实现时的实测补充（方案初稿没写到的）：
> - nhentai v2 的 `POST /galleries/{id}/download`（官方 zip）**需要 API key**（`NHENTAI_API_KEY`），没 key 就逐页下载 + 标准库 `archive/zip` 自己打
> - 官网"最新列表" `https://nhentai.net/?page=N` 有 JSON 版 `GET /api/v2/galleries?page=N`（25 条/页，匿名 15/min），比爬 HTML 稳 → 做成 `fetch_hentai` 的 `page` 参数
> - 代理（Clash）下 nhentai CDN 偶发 HTTP/2 PROTOCOL_ERROR：`fetchPath` 走 5 域名 × 2 轮阶梯（i1-i4 + v1 老域名 i.nhentai.net），封面挂了用第 1 页顶
> - yande 原图 >8MB 自动切 sample 缩放版（飞书图片 10MB 上限留余量）

---

## 1. 现状盘点

```
用户消息 → engine → fetch_image（找图/下载）→ 图片经 imageCarrier 回传模型"看" → send → 飞书
```

| 组件 | 位置 | 现状 |
|---|---|---|
| `fetch_image` | `internal/tools/image.go` | 关键词搜索（photo: Pexels→Openverse→Wikimedia；anime: Danbooru→Safebooru→nekos）/ URL 直链下载（白名单 `imageHosts`）。下载完把图回传给模型确认 |
| `send` | `internal/tools/send.go` + `internal/feishu/reporter.go` | `ImageSender`+`FileSender` 接口（tools 定义、feishu 实现）→ 自动路由：图片≤10MB 走 `/im/v1/images`，其余走 `/im/v1/files`（ch30 合并 send_image+send_file） |
| `view_image` | `internal/tools/view_image.go` | 看工作区里已有的图 |
| `netutil` | `internal/netutil/fetch.go` | 代理自动探测 + UA + LimitReader 大小上限 |

### 痛点（对应三条需求）

1. **只能发"图片"**（jpg/png/webp/gif，≤10MB）——PDF/zip 到不了飞书；
2. **"爬虫"只是直链下载**——给一个网页 URL 不会"抓整页、提取所有媒体"；
3. **图源弱**——二次元只有 Danbooru（硬编码 `rating:general` 强制 SFW，`r18` 关键词在 `danbooruTags()` 的 skip map 里被直接丢掉）+ Safebooru + nekos；没有 yande.re（最大 NSFW 库）；没有 nhentai（本子）。

---

## 2. 目标（验收标准）

| # | 场景 | 期望 |
|---|---|---|
| A | 飞书里发任意 URL（文章/产品页/漫画页） | 机器人抓整页，提取全部图片 + PDF，图片发 image 消息、PDF 发 file 消息，都在飞书客户端 |
| B | "给我来张 xxx r18 图" | 走 NSFW 源（yande.re 主力 + Danbooru r18），不再被 SFW 限制 |
| C | "抓 nh 12345" / "搜 sonohara 的本子" | nhentai v2 API：封面 + 元信息 + 全文，整本合成一个 PDF 发过来（不加密，手机可预览） |
| D | 回归 | 原有 photo / anime(SFW) 行为不变 |

---

## 3. 总体架构（四层，每层独立可发布）

```
飞书客户端
   ▲  image 消息 / file 消息(pdf,zip) / post 富文本网格
   │  internal/feishu/reporter.go   （SendImage 已有 + SendFile 新增）
工具层   fetch_image(扩展)  fetch_page(新)  fetch_hentai(新)  send(ch30合并)  view_image(不动)
   │
图源层   internal/sources/nhentai.go(新)  + tools 内 yande/konachan/danbooru(r18 开关)
   │
基础设施  netutil: FetchWith(自定义 UA/Referer)、按域名限流、白名单拆分
```

设计原则沿用现有套路：**接口定义在 tools（消费方），实现放 feishu（提供方）**——`ImageSender` 的同一套路复制一份给文件，终端外壳（`cmd/robot`）没有 sender 时优雅降级。

---

## 4. 逐模块设计

### 4.1 M1 飞书文件投递（`send_file`，ch30 后并入 `send`）

- `internal/tools/send_image.go`（ch30 时已并入 `send.go`）：新增接口（`ImageSender` 不动，避免破坏现有调用方）
  ```go
  type FileSender interface {
      SendFile(ctx context.Context, absPath, fileName, fileType string) error
  }
  // + WithFileSender(ctx, s) / fileSenderFrom(ctx)   // 与 WithImageSender 同款
  ```
- `internal/tools/send_file.go`（新，ch30 时与 send_image.go 合并为 `send.go`）：`send_file` 工具。参数 `path` + 可选 `name`；`fileType` 按扩展名自动推断：`.pdf→pdf`、`.zip→stream`（zip 在飞书走 stream 类型）、`.mp4→mp4`、`.doc/xls/ppt`、其余→`stream`。
- `internal/feishu/reporter.go`：实现 `SendFile`
  ```go
  resp := r.client.Im.File.Create(ctx, larkim.NewCreateFileReqBuilder().
      Body(larkim.NewCreateFileReqBodyBuilder().
          FileType(larkim.CreateFileFileTypePdf).   // "pdf"/"stream"/"mp4"...
          FileName("title.zip").
          File(f).Build()).Build())
  // → file_key → reply msg_type "file" content {"file_key": "..."}
  ```
- `internal/feishu/bot.go` `onMessage`：`runCtx = tools.WithFileSender(runCtx, reporter)`（在现有 `WithImageSender` 旁边加一行）。
- **SDK 已验证**：`go.mod` 里的 lark SDK v3.12.2 有 `Im.File.Create`、`CreateFileFileTypePdf/Stream/Mp4/Doc/Xls/Ppt`（`service/im/v1/model.go:189`）。
- **权限**：`im:resource` 已开通（见 `docs/feishu-setup.md` §8），**不用新开权限、不用重新发版**。
- **限制**：图片 ≤10MB，文件 ≤30MB。

### 4.2 M2 NSFW 图源（`fetch_image` 扩展）

**实测结论（2026-10-25）**：
- **yande.re 主力**：`GET https://yande.re/post.json?tags=...&rating=s|q|e&limit=N`
  返回 `file_url`（`https://files.yande.re/image/{md5}/{文件名}.png`）、`sample_url`、`preview_url`、`tags`、`file_size`、`file_ext`。`rating=e` 即 explicit（纯 R18）、`q` questionable、`s` safe。纯 HTTP 直连可用，无需 key。
- **Danbooru r18**：现有 `searchDanbooru` 硬编码 `rating:general`，`danbooruTags()` 的 skip map 把 `r18/nsfw/explicit` 关键词丢掉 → 加 `rating` 参数即可复用现有标签解析。
- **Konachan/Sankarea（兜底）**：与 Danbooru 同协议（`/post.json`）。注意 konachan 在 Cloudflare JS challenge 之后（实测返回 "Just a moment..."），纯 HTTP 客户端可能被拦 → 放链路末位或做成可选开关。

**`fetch_image` 定义变更**：
- 新增 `rating` 参数：`enum: [general, questionable, explicit]`，默认 `general`。**不新增 source 值**（r18 = `source:anime` + `rating:explicit`），减少模型决策面。
- 链路：
  - `general`（现状不变）：Danbooru → Safebooru → nekos
  - `explicit`：**yande(re: e) → Danbooru(rating:explicit) → Safebooru → nekos**
  - `questionable`：yande(rating:q) → Danbooru(rating:u) → ...
- `imageHosts` 白名单追加：`files.yande.re`、`assets.yande.re`、`yande.re`、`files.konachan.com`、`files.sankarea.eu`（+ M4 的 nhentai CDN）。
- 新增 `internal/tools/image_sources.go`（新文件，避免 image.go 继续膨胀）：`searchYande`、`searchKonachan`、`searchSankarea`，与现有 `searchDanbooru` 同签名 `func(ctx, keyword) ([]imageCandidate, error)`。

### 4.3 M3 通用爬虫（`fetch_page`）

新 `internal/tools/fetch_page.go`：

- **输入**：`url`（必填）、`max_images`（默认 8）、`want_pdf`（默认 true）。
- **流程**：
  1. `FetchWith` 拉 HTML（≤1.5MB，浏览器 UA，因为很多站对 Go UA 403）；
  2. 用 `golang.org/x/net/html` 解析（新增依赖，小、纯 Go、标准做法；备选：正则提取器，零依赖但脆弱）；
  3. 提取（按优先级）：
     - `og:image` / `twitter:image` meta → 主图（优先级 0）；
     - 全部 `<img src/srcset/data-src>`：相对 URL 用 `url.ParseRequestURI` 解析、去重（归一化查询串）、丢 `data:` base64 大图；
     - 全部 `<a href>` 以 `.pdf`（可选 `.epub/.zip`）结尾的链接；
     - `<video>/<source>`：只记录 URL 不下载（mp4 动辄几十 MB，默认只报不发）；
  4. 并行下载（信号量 4 路），存 `out/crawl/<host>/001.jpg...`（相对 `effectiveWorkDir`，群隔离自动继承）；
  5. 返回结构化清单给模型：`[图片] path / 尺寸 / 字节`、`[PDF] path / 字节`、页面 `<title>` + `og:description` 摘要；
  6. 图片走 `imageCarrier` 回传模型"先看再发"（沿用 fetch_image 的既有机制）。
- **模型行为**（写进工具 Description）：拿到清单后自己挑——用户要"全部"就逐个 `send`，要"主图"就只发 og:image；PDF 也走 `send`（自动路由 file 消息）。
- **降级**：SPA 页面（无 SSR 内容）→ 只有 og:image + title，如实告诉用户"这页是 JS 渲染的，只抓到主图"，不死循环重试。
- **安全**：抓到的媒体域名过 `mediaHosts` 白名单 + 单文件大小上限（默认 10MB 图 / 30MB PDF），防止网页里一张 50MB 的图把内存和飞书额度吃光。

### 4.4 M4 nhentai 本子（`fetch_hentai`）

**v2 API 实测**（2026-10-25；v1 已废弃，`/api/gallery/1` 现在直接返回文本 "Use new API https://nhentai.net/api/v2/docs"）：

| 端点 | 作用 | 实测要点 |
|---|---|---|
| `GET /api/v2/cdn` | CDN 域名 | `{"image_servers":["https://i1..i4.nhentai.net"],"thumb_servers":["t1..t4.nhentai.net"]}` —— **域名是动态的**，拉一次缓存 24h，兜底硬编码 i1–i4 |
| `GET /api/v2/galleries/{id}` | 详情 | `title.english/japanese`、`tags`、`num_pages`、`pages[].path`（如 `galleries/9/1.jpg`）+ `thumbnail`（`galleries/9/1t.jpg`）、`cover.path`。完整图 URL = `{image_server}/{path}` |
| `GET /api/v2/search?query=&sort=&page=` | 搜索 | 匿名 **10 次/分钟**。query 语法很强：`"exact phrase"`、`-word`、`artist:name`、`language:english`、`tag:"big breasts"`、`pages:>10`、`favorites:>=100`、`uploaded:<7d` |
| `POST /api/v2/galleries/{id}/download?format=zip\|cbz\|torrent` | **官方下载链接** | 返回 `{url, expires_at}` —— 整本一个 zip，不用自己拼 100 个页面 |
| `GET /api/v2/pow?action=` | PoW 反爬 | `{challenge, difficulty}`，SHA-256 前缀匹配，纯 CPU ~1s；被 429/challenge 时再解 |

**代码**：
- `internal/sources/nhentai.go`（新包）：`Client{CDN 缓存, PoW 求解器}`，方法 `Gallery(id) / Search(q, sort) / DownloadURL(id, format) / CDN()`。UA 用浏览器串（`netutil.FetchWith`），代理复用 `netutil.Proxy()`（国内直连 nhentai 不稳时自动走 7897 等探测端口）。
- `internal/tools/fetch_hentai.go`（新）：`fetch_hentai` 工具
  - 输入：`id_or_url`（解析 `https://nhentai.net/g/12345/` 或 `123-abc` 或裸 id）三选一 + `query`（搜索模式）；`pages`（页码范围，默认全部）；`format`（`zip` 默认 | `images` | `pdf`）。
  - 行为：
    1. 详情 → 先发**封面**（send）+ 标题/作者/标签/页数（imageCarrier 让模型确认是不是用户要的本）；
    2. 搜索模式 → 前 5 个候选（缩略图 + 标题 + 页数），模型挑一个再抓；
    3. `format=pdf`（默认）：官方 zip 一条请求取全本 → 内存解压 → `internal/pdf` 合成 PDF；没有 key/官方挂了就逐页下载再拼。超过飞书 30MB 上限自动按页拆成 part1/part2…；
    4. `format=zip`：`POST /download?format=zip` → 官方 zip（≤30MB 直发 `send`；超限降级 images 模式并说明）；
    4. `format=images`：4 路并行下载 `{image_server}/{path}` → `001.jpg...` → 逐张发（>30 张建议 post 网格，M5）；
    5. 收尾文本：标题 / artist / tags / 页数 / 来源链接。
  - 模型只看封面 + 第 1 页（不灌 100 张图给模型，imageCarrier 32MB/张 的预算省着用）。

### 4.5 M5 体验增强（可选，最后做）

- **post 富文本网格**：一条 post 消息最多塞 ~30 张 image（`msg_type: "post"`，content 段落数组里放 `{"tag":"img","image_key":...}`），整本预览不刷屏。
- **本子合并成单个 PDF**：`github.com/phly/flip`（纯 Go，图片→PDF，无 CGO），飞书客户端里能逐页翻，体验最好；100 页 zip 30MB → PDF 大小接近原图总和，注意 30MB 上限。
- **限流 + 缓存**：按域名令牌桶（yande 20/min、Danbooru 20/min + 2000/day、nhentai search 10/min）；关键词→候选结果 10 分钟内存缓存（LRU，上限 100 条）。
- **白名单透传**：`.env` 加 `MEDIA_EXTRA_HOSTS=a.com,b.com`。
- **群聊 R18 策略**：飞书客户端不自动打码。可选：群聊默认 `rating=general`、单聊默认 `explicit`（按 `msg.ChatType` 决定默认值，写进工具 Description 的提示即可，不改代码逻辑）。

---

## 5. 逐文件调整清单

### 修改（5 个文件）

| 文件 | 调整 | 里程碑 |
|---|---|---|
| `internal/tools/image.go` | ① schema 加 `rating` 参数；② `danbooruTags()` 的 skip map 按 rating 条件生效（explicit 时不丢 r18 词）；③ `searchDanbooru` 的 `rating:general` 参数化；④ `imageHosts` 追加新域名；⑤ 更新工具 Description（模型手册） | M2/M4 |
| `internal/tools/send.go` | ch30：`send_image.go`+`send_file.go` 合并为 `send.go`（`ImageSender`/`FileSender` 接口都在这，自动路由消息形态） | M1/ch30 |
| `internal/feishu/reporter.go` | 实现 `SendFile`（`Im.File.Create` → file_key → file 消息） | M1 |
| `internal/feishu/bot.go` | `onMessage` 里 `runCtx = tools.WithFileSender(runCtx, reporter)` | M1 |
| `internal/netutil/fetch.go` | 加 `FetchWith(ctx, url, opt)`（自定义 UA/Referer/Accept/MaxBytes），现有 `Fetch` 变成它的薄封装；（M5：按域名限流器） | M3/M4 |
| `cmd/bot/main.go` + `cmd/agentops/main.go` | 注册新工具：`NewSendTool`（ch30 前是 `NewSendImageTool`+`NewSendFileTool`）、`NewFetchPageTool`、`NewFetchHentaiTool` | M1-M4 |

### 新增（4 个文件）

| 文件 | 内容 | 里程碑 |
|---|---|---|
| `internal/tools/send.go` | `send` 工具（ch30 合并：扩展名+大小自动路由 image/file 消息；扩展名→fileType 推断） | M1/ch30 |
| `internal/tools/image_sources.go` | `searchYande` / `searchKonachan` / `searchSankarea`（与现有 search 函数同签名） | M2 |
| `internal/tools/fetch_page.go` | 通用爬虫（HTML 解析 + 媒体提取 + 并行下载 + 清单回传） | M3 |
| `internal/sources/nhentai.go` + `internal/tools/fetch_hentai.go` | nhentai v2 客户端 + 工具 | M4 |

### 新增依赖（go.mod）

- `golang.org/x/net`（html 解析，M3 必需）
- `github.com/phly/flip`（图片→PDF，仅 M5 可选）

### 不动的（回归保障）

- `internal/schema`（AGENTS.md 铁律：协议字段名不动）
- `engine/loop.go` 的 imageCarrier 机制、`view_image`、`send`（原 send_image）现有行为
- 飞书后台：**`im:resource` 已开，不用重新发版**

---

## 6. 风险与对策

| 风险 | 对策 |
|---|---|
| nhentai Cloudflare / PoW 反爬 | 浏览器 UA 直连 `/api/v2`（实测通过）；429 再解 PoW（SHA-256 前缀，纯 CPU ~1s）；终极兜底镜像站（e-hentai / allhentai） |
| Konachan CF challenge（"Just a moment..."） | 放链路末位或 env 开关；主力是 yande.re |
| yande 的 `file_url` 文件名 = 整串 tags（很长） | 落盘时重命名为 `{md5前8位}_{序号}.jpg`；飞书 `file_name` 截断 80 字符 |
| 单文件 >30MB（飞书 file 上限） | zip 模式超限自动降级 images 模式；PDF 模式用 flip 可轻度压缩 |
| SPA 页面无 SSR | `fetch_page` 降级 og:image + title，如实告知，不重试 |
| 群聊 R18 尴尬 | 群聊默认 general / 单聊默认 explicit（可选策略） |
| 搜索限流（nh 10/min、yande ~20/min） | 按域名令牌桶 + 10 分钟结果缓存 |
| v1 API 已废弃 | 全部走 v2（实测 v1 返回 "Use new API" 文本，直接 JSON 解析会炸） |
| 模型把 100 页全灌给自己 | 只看封面 + 第 1 页，工具 Description 里写明 |

---

## 7. 里程碑（每个独立可发布、可验收）

| 里程碑 | 内容 | 状态 | 验收 |
|---|---|---|---|
| **M1** 文件投递 | `FileSender` + `send_file`（ch30 并入 `send`）+ `reporter.SendFile` + bot 装配 | ✅ ch27 | `send out/report.pdf` → 飞书收到 file 消息 |
| **M2** NSFW 图源 | yande 源 + Danbooru rating 开关 + `rating` 参数 + 白名单 | ✅ ch28 | "给我来张 cat_ears r18 图" → 收到 R18 图 |
| **M3** 爬虫 | `fetch_page`（x/net/html + 提取 + 并行下载） | ⏳ | 发任意文章 URL → 图片 + PDF 到飞书 |
| **M4** nhentai | v2 客户端 + `fetch_hentai`（id/query/page）+ zip 模式 + CDN 阶梯 | ✅ ch29 | "抓 nh 12345" / "搜 sonohara" / "翻第 N 页最新" → 封面 + 整本 zip |
| **M5** 体验（可选） | post 网格、flip 合并 PDF、限流缓存、env 白名单、netutil 直连兜底 | ⏳ | 整本一条网格消息 / 单个 PDF 逐页翻 |

建议顺序 M1 → M2 → M4 → M3（M4 是用户点名要的，M3 通用爬虫排最后），M5 看心情。

---

## 8. 明确不做（范围控制）

- **e-hentai/exhentai**：要账号 + `itp` 校验，复杂度翻倍，等 nhentai 不够用再说；
- **视频下载**：mp4 在 M3 只"记录 URL"，默认不下载（飞书 file 消息支持 mp4，但动辄几十 MB）；
- **登录态**：nhentai 收藏、e-hentai 标签库（需要 API key，v2 已支持 `Key <api_key>` 头）；
- **接收方向**：用户发图/PDF 给机器人（要处理 `image`/`file` 消息事件 + `im:resource` 下载，`docs/feishu-setup.md` §7 已列，独立做）。

---

## 搜索为什么老是 0 条（ch44 实测）

线上事故：用户说「帮我找鸣潮小爱弥斯的 R18 本子，中文」，模型连续 82 轮用
`aysa wuthering_waves chinese` 搜同一个词，全部 0 条，烧了 18 分钟。

### 三个叠加原因

1. **nhentai 的 query 是"多词 AND"**：`wuthering_waves chinese` = 0 条，
   但 `wuthering waves` = 77 条。多一个语言词就把结果清零。
2. **下划线不认**：`wuthering_waves` 0 条，`wuthering waves` 77 条。
3. **名字猜错**：模型把「爱弥斯」猜成 `aysa`/`ayshea`，正确是 `aemeath`
   （`aemeath` 5 条 —— 而且其中一条标题就是「爱弥斯 (Wuthering Waves)」）。
4. **打转检测失效**：旧实现只在 `IsError` 时计数，"成功但 0 条"直接清零，
   所以 82 轮都没人拦。

### 现在的四层兜底

| 层 | 做什么 | 依据 |
|---|---|---|
| 1 | 原词搜索 | — |
| 2 | 名字解析：本地别名表（`爱弥斯→aemeath`、`伊莉雅→illyasviel`…） | 内置表 + `workspace/.claw/aliases.json`（用户可自己加） |
| 3 | 作品反查：用作品名搜到本子，读它的 `character` 标签 | nhentai 自己的数据，不引新源 |
| 4 | 写法降级：去语言词 → 去下划线 → 逐词单独搜 | 实测 `wuthering waves` 77 条、`wuthering_waves` 0 条 |

每层结果都缓存（含"解析不出"的负结果），避免撞 10 次/分钟限流；
降级链一次最多 4 个词。

### 为什么不用子 agent 做这件事

同一个模型、同一个知识盲区：它猜 `aysa` 猜错，派个子 agent 还是猜 `aysa`，
只是多一次调用、多几十秒、多一份 token。**名字解析需要的是数据（对照表/站点标签），
不是推理。** 子 agent 适合"拆任务"，不适合"补知识"。
