# 飞书机器人接入指南（go-tiny-claw / little-robot）

> 这份文档记录的是**实际走通一遍**的流程，不是官方文档的复述。
> 包含：后台配置的每一步、四个必须开的权限、发布版本的时机、验证方法、踩过的七个坑，
> 以及**用 Playwright 让 Agent 帮你操作后台**的辅助方案。
>
> 对应代码：`internal/feishu/bot.go`、`internal/feishu/reporter.go`、`cmd/bot/main.go`。
> 对应章节：ch09（Reporter 解耦 + 飞书长连接）。

---

## 0. 前置概念：五个名词

| 名词 | 是什么 | 在你的项目里 |
|---|---|---|
| **自建应用**（Custom App） | 建在某个“企业/组织”下的应用，仅该组织内可用 | `测试智能体LITTLE-ROBOT` |
| **企业/组织**（tenant） | 飞书里的一级组织，个人也能免费创建 | `ddcompany` |
| **长连接**（WebSocket） | 你的进程主动连飞书，飞书把事件推过来 | 不需要公网 IP / 域名 / 内网穿透 |
| **权限**（scope） | 你**能做什么**（读消息 / 发消息 / 读群信息） | `im:message.group_msg` 等 5 条 |
| **事件订阅**（event） | 飞书**推什么给你** | `im.message.receive_v1` |

### 三个开关的关系（最容易搞混）

```
权限 scope    → 你能读写什么
事件订阅       → 飞书把什么推到你的长连接（消息、入群…）
回调订阅       → 飞书把什么"用户交互"推给你（点卡片按钮…）
发布版本       → 以上配置什么时候生效
```

**三者缺一不可，而且每次改配置都要重新发版。** 这是后面所有坑的总根源。

### 应用身份 vs 用户身份

| 身份 | 凭证 | 适用场景 |
|---|---|---|
| **应用身份** | `tenant_access_token` | 无人值守：机器人自己干活 ← **本项目全部用这个** |
| 用户身份 | `user_access_token` | 需要真人登录授权，代表他操作 |

权限管理页右上角有两个 tab，**别在用户身份下开权限**——开了也用不上，还会让排查变复杂。

⚠️ 个人开发者**可以**走完全程：用手机号注册 → 创建一个免费团队（哪怕只有你一个人）→ 你就是管理员 → 可以建应用、开权限、自己审批发布。**不需要营业执照**（只有“对外部租户发消息”“上架应用商店”这类能力才要企业认证）。

---

## 1. 配置流程（照着勾）

### 1.1 账号与组织

- [ ] `feishu.cn` 用手机号注册
- [ ] 创建一个**团队**（免费版，如 `little-robot-lab`），跳过邀请成员
- [ ] 打开 `open.feishu.cn/app`，**右上角切换到该团队**
      （停在“个人版”身份会提示「请先创建企业」）

### 1.2 建应用

- [ ] 「创建企业自建应用」→ 填名称 / 描述 / 图标
- [ ] ⚠️ 选 **从空白开始**，**不要**用「智能体模板」
      模板会预置成 HTTP 回调模式，那就需要公网地址了；我们要的是长连接。

### 1.3 开机器人能力

- [ ] 「添加应用能力」→ **机器人** → 添加 / 启用

### 1.4 开权限（**6 条，分三层**）

「权限管理」→「开通权限」→ 按**中文名称**搜索（按 code 搜反而不准）→ 勾选 →「确认开通权限」。
⚠️ 页面右上角有两个 tab，**确认自己在「应用身份权限」下**。

#### 第一层：底座（缺了整个机器人跑不起来）

| # | 权限 code | 名称 | 不开会怎样 |
|---|---|---|---|
| 1 | `im:message` | 获取与发送单聊、群组消息 | 消息收发全不通（历史总括权限，留着别删） |
| 2 | `im:message:send_as_bot` | 以应用的身份发消息 | 能收不能回 |

#### 第二层：收消息的入口（按场景一条条加）

| # | 权限 code | 名称 | 对应场景 |
|---|---|---|---|
| 3 | `im:message.p2p_msg:readonly` | **读取用户发给机器人的单聊消息** | 单聊 |
| 4 | `im:message.group_at_msg:include_bot:readonly` | 获取群组中其他机器人和用户@当前机器人的消息 | 群里 @ 它 |

> **为什么第 3 条最容易漏**：添加「接收消息」事件时，飞书只会自动帮你补第 4 条（群内 @）。
> 单聊需要的 `p2p_msg:readonly` 不会自动加，而缺了它，**单聊窗口连对话框都不给**。
> 官方 FAQ：<https://open.feishu.cn/document/faq/bot.md>

#### 第三层：功能权限（可选，按体验需求加）

| # | 权限 code | 名称 | 对应场景 |
|---|---|---|---|
| 5 | `im:message.group_msg` | 获取**群组中所有消息**（**敏感权限**） | 群里**不 @** 也响应 |
| 6 | `im:chat:readonly` | 获取**群组信息**（群名、群类型） | 把会话工作目录命名成群名（`chats/dd-f1a826`），比 `oc_xxx` 可读 |

> **`group_msg` 敏感在哪**：它让应用能读到群里的全部聊天记录，哪怕没 @ 它。商店应用上架时大概率被驳回；自建应用 + 免审团队一般能一键开通。
>
> **为什么不用 `group_msg:include_bot:readonly`**：那条会连**机器人自己发的消息**一起推，容易形成自触发循环。用 `group_msg` 天然避开了这个坑。
>
> **`im:chat:readonly` 还顺带解决两件事**：列群（`GET /im/v1/chats`）、判断机器人在不在某个群。缺它时这些接口一律返回 `99991672`。

#### 三条路线，按需选

| 你想要的效果 | 需要的权限 | 条数 |
|---|---|---|
| 只能单聊 | 1 + 2 + 3 | 3 |
| 单聊 + 群里 @ | 1 + 2 + 3 + 4 | 4 |
| 单聊 + 群里不 @ 也回 | 1 + 2 + 3 + 4 + 5 | 5 |
| 想要可读的工作目录名 | 再加 6（`im:chat:readonly`） | 6 |

> 权限条数不是越多越好：**只开你真正调用的接口**。`im:chat:readonly` 是"体验优化"，不是"能力前提"——不开也能跑，只是目录叫 `oc_xxx`。

### 1.5 事件订阅（必须点保存）

- [ ] 「事件与回调」→ 事件配置 → 订阅方式选 **使用长连接接收事件**
- [ ] **点「保存」**（不保存，下面的「添加事件」按钮永远是灰的）
- [ ] 「添加事件」→ 搜「接收消息」→ 勾 `im.message.receive_v1` → 添加
- [ ] 弹窗提示「推荐开通以下权限」→ 点**确认开通权限**（会补上第 4 条）

> `group_msg` 不需要新增事件，它只是让**同一个** `im.message.receive_v1` 推得更多。

### 1.5.1 回调配置（要卡片按钮就必须配，ch39-ch41）

> 事件是"飞书告诉你发生了什么"，回调是"用户点了你卡片上的按钮，你得**立刻**回一个响应"。
> 两者是**两套独立配置**，只配事件的话，卡片按钮点下去毫无反应、群里也不会报错。

- [ ] 「事件与回调」→ **回调配置** 标签页
- [ ] 「订阅方式」右侧的**铅笔按钮** → 选 **使用长连接接收回调** → **保存**
      （此时「订阅方式」会显示"使用 长连接 接收回调"，「添加回调」按钮才会从灰变蓝）
- [ ] 「添加回调」→ 勾 **卡片回传交互**（`card.action.trigger`）→ **添加**
      > 别勾「消息卡片回传交互（旧）」（`card.action.trigger_v1`），旧版回调结构不同；
      > 本项目用的是新版 `callback.CardActionTriggerEvent`。
- [ ] ⚠️ **仍然要去发一版**（同 1.6）：改完回调配置，页面顶部会显示
      「版本发布后，当前修改方可生效」，不发布 = 按钮照旧没反应

**实测记录（2026-10-26，用 Playwright 操作）**：

| 步骤 | 页面状态变化 |
|---|---|
| 进「回调配置」 | 订阅方式：未配置；已订阅的回调：暂无数据；「添加回调」是灰的 |
| 点铅笔 → 选长连接 → 保存 | 订阅方式变成"使用 长连接 接收回调" |
| 添加回调 → 勾卡片回传交互 | 列表出现 `卡片回传交互 card.action.trigger` |
| 创建版本 1.0.5 → 确认发布 | 顶部变成"当前修改均已发布"，版本详情"已发布" |

**怎么验证真的通了**：在群里发 `自检`，看第一行——
`卡片回调：❌ 一次都没收到过` 就是没通；点一下控制台卡片上的按钮后变成
`✅ 已收到 1 次` 就是通了（代码里 `Bot.cardCallbacks` 计数器）。

### 1.6 发布版本（不发布 = 前面全白配）

- [ ] 「版本管理与发布」→「创建版本」→ 版本号 `1.0.0` → 更新说明随便填
- [ ] **保存** → **确认发布**（自建应用免审核，你自己就是审批人）

> ⚠️ **铁律：每次改权限 / 改事件 / 改机器人配置，都要重新发一版。**
> 这是「明明配了却不生效」的头号原因，占所有排查的一半。
> 本次实测：补 `group_msg` 后发了 1.0.2，下一条群消息才按新规则推送。


### 1.7 拿凭证写 .env

- [ ] 「凭证与基础信息」→ 复制 **App ID**（`cli_` 开头）和 **App Secret**
      （Secret 默认打码，点右边的**复制**按钮，不要点眼睛图标，避免明文上屏）
- [ ] 写进项目根目录 `.env`：

```bash
FEISHU_APP_ID=cli_xxxxxxxx
FEISHU_APP_SECRET=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

- [ ] 确认 `.gitignore` 里有 `.env` 和 `/workspace/`

### 1.8 把机器人拉进会话

- [ ] **单聊**：工作台 → 点应用图标 → 直接进单聊（任何文本都能收到，不用 @）
- [ ] **群聊**：群里加机器人。默认**必须 @ 它**才会推事件；开了 `group_msg` 后不 @ 也推
- [ ] 单聊不需要 @

### 1.9 群聊到底要不要 @（开了 `group_msg` 之后）

| 模式 | 需要 `group_msg` | 体验 | 代价 |
|---|---|---|---|
| 群里 @ 它 | ❌ | 每次多按两个键 | 无 |
| 群里不 @ 也回 | ✅ | 像真人一样接话 | **群里每条消息都触发一次 LLM 调用** |

开了 `group_msg` 之后会立刻出现两个新问题：

**① 成本**：群里一句“中午吃啥”也会走一次完整上下文（system prompt + 历史，几千 token）。活跃群一天几十块很正常。

**② 刷屏**：它会对闲聊认真作答，群里会很尴尬。

**不会自触发循环**——`group_msg` 不包含机器人自己发的消息（那个要 `group_msg:include_bot:readonly`，别开）。

建议的最小防护（不做内容过滤，只限制生效范围）：

```go
// internal/feishu/bot.go 的 onMessage 里
if deref(msg.ChatType) == "group" && chatID != os.Getenv("AGENTOPS_CHAT_ID") {
	return nil
}
```

`.env` 里填上那个群的 `oc_xxx`（终端日志 `[Feishu] 收到消息 chat=oc_xxx` 里就有）。

---

## 2. 代码结构

| 文件 | 职责 | 依赖方向 |
|---|---|---|
| `internal/engine/reporter.go` | 定义 `Reporter` 接口：引擎只发事件，不管展示 | engine 不依赖任何外壳 |
| `internal/feishu/reporter.go` | 实现 `Reporter`，把事件变成飞书消息 | feishu → engine |
| `internal/feishu/bot.go` | 收事件 → 去重 → 会话串行 → 后台跑 `eng.Run` | feishu → engine |
| `internal/guard/deny.go` | 无人值守审批器：高危操作一律拒绝 | guard → tools |
| `internal/session/store.go` | 会话落盘 `.claw/sessions/<id>.json`，支持断点续传 | session |
| `internal/runstate/runstate.go` | **本次运行**的可变状态（工作目录覆盖 / 方案 / 审批），走 ctx 传，不落盘 | 谁都不依赖 |
| `internal/worktree/worktree.go` | `git worktree` 管理：按任务开目录 + 分支，能 diff 能丢弃 | 只依赖 git |
| `cmd/bot/main.go` | **最小示例**：只有 provider + 四个工具 | 同时认识两边 |
| `cmd/agentops/main.go` | **生产形态**：tracker + guard + 子代理 + 持久会话 | 同时认识两边 |

### 工作目录怎么分（多群不互相踩）

详见 [工作目录隔离](./workdir-isolation.md)。一句话版本：

```
session.Session  = 档案柜（跨消息、跨重启）
engine.RunState  = 台面（只在一次 Run 内有效，比如这次在哪个目录干活）
```

`internal/feishu/bot.go` 每条消息进来先算目录：

```go
rs := runstate.New()
rs.WorkDir = b.workDirFor(ctx, chatID)   // chats/<群名>-<短id>，或 .worktrees/chat-...
runCtx = runstate.With(runCtx, rs)
```

工具侧统一走 `effectiveWorkDir(ctx, t.workDir)` 取目录——**子代理自动继承**，因为它拿的是同一个 ctx。

### 四个必须处理的工程点

1. **事件去重**：飞书没收到成功响应会重推，按 `message_id` 去重，否则模型会把同一件事做两遍；
2. **handler 立刻返回**：只在 handler 里登记 + 起 goroutine，不要同步跑 Agent（长连接 SDK 是串行处理的，堵住会积压）；
3. **同一会话串行**：每个 `chat_id` 一把 `sync.Mutex`，忙时回「稍后再发」，避免两个任务同时改同一个工作区；
4. **不继承 handler 的 ctx**：goroutine 里用 `context.Background()` + 自己的超时，否则 handler 一返回 ctx 被取消，任务半路崩。

### 审批器必须跟着场景换

| 场景 | Approver | 理由 |
|---|---|---|
| 终端（`cmd/robot`） | `TerminalApprover` | 人在键盘前，能当场回答 |
| 飞书（`cmd/agentops`） | `DenyApprover` | 无人值守，`TerminalApprover` 会永久阻塞在 `os.Stdin` |
| 评测（`cmd/bench`） | 不挂 guard | 无人值守且命令都是自己写的 |

**同一个 `guard.Middleware`，三种策略**——这就是 `Approver` 接口存在的意义。


---

## 3. 验证顺序（从下往上，逐层排除）

```bash
# ① 凭证对不对（不需要任何代码）
curl -s -X POST https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal \
  -H "Content-Type: application/json" \
  -d '{"app_id":"cli_xxx","app_secret":"xxx"}'
# 期望：{"code":0,...,"tenant_access_token":"t-..."}

# ② 长连接能不能连上
go run ./cmd/bot
# 期望：[Feishu] 长连接启动中... + connected to wss://msg-frontier.feishu.cn

# ③ 消息能不能收到（看终端）
# 期望：[Feishu] 收到消息 chat=oc_xxx: ...

# ④ 消息能不能发回（看飞书客户端）
# 期望：先收到「🔧 正在执行 xxx」，再收到模型的总结
```

**哪一层断了就查哪一层**，不要跳着猜。

### 飞书侧的证据：事件日志

`开发者后台 → 运营监控 → 日志检索 → 事件日志检索` → 选时间范围 → 查询。

- 有记录：说明飞书推了，问题在你的接收侧；
- **「暂无查询结果」：说明飞书压根没推**，问题在权限 / 事件订阅 / 版本发布三者之一。

这一条能把“代码问题”和“配置问题”一刀切开，是最省时间的排查入口。

---

## 4. 十三个坑

| 现象 | 原因 | 处理 |
|---|---|---|
| 单聊没输入框 / 收不到 | 缺 `im:message.p2p_msg:readonly` | 补权限 + **重新发版** |
| 群里 @ 才理我 | 群聊规则如此；要收全量需 `im:message.group_msg`（敏感） | 用单聊，或申请该权限 |
| 配了权限还是不回 | 没重新发布版本 | 每次改配置都发版 |
| 有 `[Feishu] 收到消息` 但不回 | 缺 `send_as_bot`；或 `content` 不是合法 JSON | 看有没有 `[Feishu] 回复失败：...` |
| 消息时有时无 | 同时起了两个 bot 进程，抢同一条长连接 | 只留一个进程 |
| 事件日志「暂无查询结果」 | 飞书没推 | 回到权限 / 事件 / 版本三项 |
| 改了 `.env` 不生效 | 环境变量在进程启动时读入，`os.Getenv` 不重读文件 | **重启进程** |
| 开了 `group_msg` 后群里每句话都在烧钱 | 群消息全量推送 | 加 `AGENTOPS_CHAT_ID` 白名单，或退回「只 @ 模式」 |
| 机器人对闲聊认真作答、刷屏 | 全量模式没有意图判断 | 同上；或改用关键词前缀（`robot:`） |
| 机器人回复自己、无限循环 | 开了 `group_msg:include_bot:readonly` | 改回 `group_msg`（不含机器人消息） |
| 群改名后工作目录"空了" | 目录名跟着群名变，老文件留在旧目录 | 用 `chats.json` 固化映射，首次命名后不再变 |
| 目录名建不出来 | 群名里有 `/`、`:`、emoji | 名字清洗：只留中英文/数字/`-_`，其余转 `-` |
| worktree 一直不启用 | 工作区不是 **git 仓库根**（只是仓库里的子目录） | 正常现象，自动降级为 `chats/` 子目录；要用 worktree 就把工作区指到独立仓库 |

### 一个隐蔽的坑：`content` 必须是合法 JSON

SDK 的 `larkim.NewTextMsgBuilder().Text(s).Build()` 是**裸字符串拼接**，不转义引号和反斜杠。
模型输出里只要带一个 `"`，拼出来的就是非法 JSON，发送直接失败。

```go
// ❌ 模型输出含引号时会炸
larkim.NewTextMsgBuilder().Text(text).Build()

// ✅ 用 json.Marshal，自动转义
content, _ := json.Marshal(map[string]string{"text": text})
```

---

### 坑：卡片按钮点了没反应（ch39 引入）

现象：卡片发出来了、按钮能点，但群里没有任何反应，日志里也没有 `[Feishu] 卡片动作`。

原因排序（按实际排查顺序）：

1. **回调配置没做**：只配了事件、没配回调。事件和回调是两套独立开关（见 1.5.1）
2. **配了但没发版**：顶部还挂着"版本发布后，当前修改方可生效"
3. **订阅方式选错**：选成"将回调发送至开发者服务器"却没填 URL —— 长连接模式下不要选它
4. **勾了旧版回调**：勾成 `card.action.trigger_v1`，SDK 收到的是另一套结构，
   `OnP2CardActionTrigger` 不会触发

自检办法：群里发 `自检`，看 `卡片回调：✅/❌`。详情见 [feishu-control.md](./feishu-control.md)。

## 5. 用 Playwright 辅助配置后台（可选）

后台配置是**纯 UI 操作**，而且有很多“必须点保存 / 必须发版”的隐式规则，手点很容易漏。
另一种做法是：让一个 Agent 起一个**可见的浏览器**，你用截图+读图的方式盯着它点，或者它帮你点。

### 5.1 能做到什么

- 打开一个独立的可见 Chrome 窗口（不影响你日常用的 Chrome）；
- 按元素角色精确点击按钮、往输入框填值、勾选 checkbox；
- 随时截图，把「当前页面长什么样」交给视觉模型判断；
- 读出页面文本 / DOM，确认按钮是否 `disabled`、勾选是否生效。

### 5.2 做不到什么（务必知道）

- **登录必须由人完成**：扫码 / 短信验证码，Agent 不接触账号密码；
- 控制台是动态渲染的，选择器会变，脚本需要按实际页面调；
- 不要用它做“无人值守的批量操作”——每一步都该有人确认。

### 5.3 原理

```
node launch.js
   ├─ playwright 以 channel:"chrome" 启动可见浏览器（用系统 Chrome，不下载 chromium）
   ├─ 使用独立 user-data-dir（登录态持久化，重启浏览器不用重新扫码）
   └─ 在 127.0.0.1:7788 起一个极简 HTTP 控制口
          GET  /state      → 当前 URL / 标题 / 标签页
          GET  /shot.png   → 截图
          GET  /text       → 页面 innerText
          POST /do         → {a:"goto"|"click"|"fill"|"clickxy"|"clickbox"|"eval", ...}
```

Agent 侧只需要 `curl` + 读截图，不需要浏览器协议知识。

### 5.4 脚本（保存为 `launch.js`，在装好 playwright 的目录里运行）

```javascript
const http = require('http');
const { chromium } = require('playwright');

(async () => {
  const ctx = await chromium.launchPersistentContext('/tmp/feishu-profile', {
    channel: 'chrome',          // 用系统 Chrome，无需 playwright install
    headless: false,            // 必须可见，人要扫码登录
    viewport: { width: 1440, height: 900 },
    locale: 'zh-CN',
  });

  const page = ctx.pages()[0] || (await ctx.newPage());
  await page.goto('https://open.feishu.cn/app', { waitUntil: 'domcontentloaded' }).catch(() => {});

  const active = () => {
    const ps = ctx.pages();
    return ps[ps.length - 1] || page;
  };

  const clickByName = async (p, name, idx) => {
    // 优先按“按钮角色 + 精确名字”找，避免匹配到标题里的同名文字
    const btn = p.getByRole('button', { name, exact: true });
    const n = await btn.count();
    if (n) {
      const i = idx === undefined || idx === null ? n - 1 : idx;
      await btn.nth(i).click({ timeout: 8000 });
      return 'clicked-button#' + i + '/' + n;
    }
    const txt = p.getByText(name, { exact: true });
    const m = await txt.count();
    if (m) {
      const i = idx === undefined || idx === null ? m - 1 : idx;
      await txt.nth(i).click({ timeout: 8000 });
      return 'clicked-text#' + i + '/' + m;
    }
    throw new Error('not found: ' + name);
  };

  const server = http.createServer(async (req, res) => {
    try {
      const u = new URL(req.url, 'http://127.0.0.1');
      const p = active();

      if (u.pathname === '/shot.png') {
        const buf = await p.screenshot();
        res.setHeader('content-type', 'image/png');
        res.end(buf);
        return;
      }

      if (u.pathname === '/state') {
        res.setHeader('content-type', 'application/json; charset=utf-8');
        res.end(JSON.stringify({
          url: p.url(),
          title: await p.title().catch(() => ''),
          pages: ctx.pages().map((x) => x.url()),
        }));
        return;
      }

      if (u.pathname === '/text') {
        const t = await p.evaluate(() => (document.body ? document.body.innerText : ''));
        res.setHeader('content-type', 'text/plain; charset=utf-8');
        res.end(t);
        return;
      }

      if (u.pathname === '/do' && req.method === 'POST') {
        let body = '';
        for await (const chunk of req) body += chunk;
        const a = JSON.parse(body || '{}');
        let out = 'ok';

        if (a.a === 'goto') {
          await p.goto(a.url, { waitUntil: 'domcontentloaded' });
          out = 'goto ' + p.url();
        } else if (a.a === 'click') {
          out = await clickByName(p, a.name, a.idx);
        } else if (a.a === 'fill') {
          // 优先在弹窗/抽屉里找输入框，避免命中页面顶部的全局搜索框
          const modal = p.locator('.ud__modal').last();
          const root = (await modal.count()) ? modal : p;
          const loc = a.area
            ? root.locator('textarea').nth(a.idx || 0)
            : root.locator('input').nth(a.idx || 0);
          await loc.fill(a.value, { timeout: 8000 });
          out = 'filled';
        } else if (a.a === 'fillph') {
          await p.getByPlaceholder(a.ph, { exact: false }).last().fill(a.value, { timeout: 8000 });
          out = 'filled-ph';
        } else if (a.a === 'clickbox') {
          // 注意：用 click() 而不是 check()。check() 对“已勾选”是 no-op，
          // 想取消全选/切换状态必须用 click()
          let root = p;
          for (const sel of ['.ud__drawer', '.ud__modal']) {
            const l = p.locator(sel).last();
            if (await l.count()) { root = l; break; }
          }
          const boxes = root.locator('input[type=checkbox]');
          const total = await boxes.count();
          await boxes.nth(a.idx || 0).click({ force: true, timeout: 8000 });
          out = 'clicked box #' + (a.idx || 0) + ' of ' + total;
        } else if (a.a === 'clickxy') {
          await p.mouse.click(a.x, a.y);   // 图标类元素（铅笔、复制）只能按坐标点
          out = 'clicked xy ' + a.x + ',' + a.y;
        } else if (a.a === 'eval') {
          out = JSON.stringify(await p.evaluate(a.expr));
        } else if (a.a === 'press') {
          await p.keyboard.press(a.key || 'Enter');
          out = 'pressed';
        } else if (a.a === 'wait') {
          await p.waitForTimeout(a.ms || 1000);
          out = 'waited';
        } else if (a.a === 'reload') {
          await p.reload({ waitUntil: 'domcontentloaded' });
          out = 'reloaded';
        } else {
          out = 'unknown action';
        }

        res.setHeader('content-type', 'text/plain; charset=utf-8');
        res.end(out);
        return;
      }

      res.statusCode = 404;
      res.end('nope');
    } catch (e) {
      res.statusCode = 500;
      res.end('ERR ' + (e && e.message));
    }
  });

  server.listen(7788, '127.0.0.1', () => console.log('CONTROL_READY', page.url()));
})();
```

### 5.5 用法

```bash
# 一次性准备
mkdir -p /tmp/feishu-console && cd /tmp/feishu-console
npm init -y && npm i playwright        # 只装库，不下载 chromium（用系统 Chrome）

# 启动（会弹出一个可见的 Chrome 窗口）
node launch.js

# 人工扫码登录后，就可以用 curl 驱动了
curl -s localhost:7788/state
curl -s localhost:7788/shot.png -o shot.png
curl -s -X POST localhost:7788/do -d '{"a":"goto","url":"https://open.feishu.cn/app/cli_xxx/auth"}'
curl -s -X POST localhost:7788/do -d '{"a":"click","name":"开通权限"}'
curl -s -X POST localhost:7788/do -d '{"a":"fillph","ph":"例如：查看群信息","value":"单聊消息"}'
curl -s -X POST localhost:7788/do -d '{"a":"clickbox","idx":1}'
curl -s -X POST localhost:7788/do -d '{"a":"click","name":"确认开通权限"}'
```

### 5.6 实战中踩到的四个细节（都是通用经验）

1. **按角色定位按钮，别按纯文本**
   `getByText("创建")` 会命中页面标题「选择应用创建方式」，然后被弹窗遮挡、点击超时。
   用 `getByRole('button', { name: '创建', exact: true })` 才准。

2. **`check()` 对已勾选元素是 no-op**
   权限列表表头那个 checkbox 是「全选」。先点全选再加单条，用 `check()` 会“什么都不做”，
   于是你以为改了、其实没改。切换状态一律用 `click()`。

3. **抽屉/弹窗里改完配置，外面按钮仍是 `disabled`**
   「添加事件」灰着，不是 bug，是因为「订阅方式」还没**保存**。
   遇到 disabled，先用 `eval` 把按钮状态打出来看，别硬点：
   ```bash
   curl -s -X POST localhost:7788/do \
     -d '{"a":"eval","expr":"[...document.querySelectorAll(\"button\")].map(b=>b.innerText.trim()+\"|\"+b.disabled).join(\";\")"}'
   ```

4. **页面滚动不在 `window` 上**
   控制台用的是内部滚动容器，`window.scrollTo()` 无效（截图看起来“没反应”）。
   优先用“按名字点击”，Playwright 会自动把元素滚进视口，比手动滚动可靠。

### 5.7 安全须知

- 控制口只监听 `127.0.0.1`，不要改成 `0.0.0.0`；
- 浏览器的 user-data-dir 里有**你的飞书登录态**，不要提交到仓库、用完删掉：

  ```bash
  rm -rf /tmp/feishu-profile /tmp/feishu-console
  ```
- 取 App Secret 时优先用页面上的「复制」按钮 + 写文件，**不要点“眼睛”图标**，
  避免密钥出现在截图和聊天记录里。

---

## 6. 附：常用命令

```bash
# 换 tenant_access_token
curl -s -X POST https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal \
  -H "Content-Type: application/json" \
  -d '{"app_id":"cli_xxx","app_secret":"xxx"}'

# 机器人所在群列表（需要 im:chat:readonly，可选）
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://open.feishu.cn/open-apis/im/v1/chats?page_size=20"

# 主动发消息到群（receive_id 是 oc_ 开头）
curl -s -X POST "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=chat_id" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"receive_id":"oc_xxx","msg_type":"text","content":"{\"text\":\"hello\"}"}'
```

---

## 7. 后续可做的增强

| 增强 | 说明 | 对应章节 |
|---|---|---|
| 卡片按钮审批 | 高危 `bash` 前弹「✅ 执行 / ❌ 取消」，代替 `DenyApprover` 的一刀切 | ch16 |
| 主动推送 | `Im.Message.Create` + 定时器，定时汇报 | — |
| 会话记忆 | ✅ 已实现：`session.NewPersistentManager` + `Bot.UseSessions` | ch11 / ch21 |
| 上下文压缩 | ✅ 已实现：`session.NewCompactor` | ch12 |
| 成本总账 | `tracker.Report()` 目前只在退出时打日志，可加 `/cost` 指令或每日推送 | ch18 |
| 图片/文件消息 | 处理 `message_type` 为 `image`/`file` 的事件（还需 `im:resource` 权限下载） | — |
| 群白名单 | `AGENTOPS_CHAT_ID` 限定生效范围，见 §1.9 | — |

## 8. 权限速查（本项目实测结果）

当前应用（`cli_aa50ac247d789be1`）已开通 **7 条**，全部为**应用身份**：

```
im:message                                 总括：消息读写
im:message:send_as_bot                     发消息
im:message.p2p_msg:readonly                收单聊
im:message.group_at_msg.include_bot:readonly   收群里 @ 我的
im:message.group_msg                       收群里所有消息（敏感权限）
im:resource                                上传/下载图片资源（发图、读图）
im:chat:readonly                           查群信息（工作目录命名、列群）
```

版本：`1.0.4`。**每次改权限都要重新发版。**

没开、按需再补：

| 权限 | 用途 | 缺失时的表现 |
|---|---|---|
| `contact:user.base:readonly` | 查用户姓名/部门 | 只能用 open_id 称呼人 |
| `im:message:send_as_user` | 以**用户身份**发消息 | 只能以机器人身份发 |

> `99991672` 的含义是「缺 scope」，飞书会把需要的权限名一起返回，照着去开 + **重新发版**。
