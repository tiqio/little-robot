# 飞书机器人接入指南（go-tiny-claw / little-robot）

> 这份文档记录的是**实际走通一遍**的流程，不是官方文档的复述。
> 包含：后台配置的每一步、四个必须开的权限、发布版本的时机、验证方法、踩过的七个坑，
> 以及**用 Playwright 让 Agent 帮你操作后台**的辅助方案。
>
> 对应代码：`internal/feishu/bot.go`、`internal/feishu/reporter.go`、`cmd/bot/main.go`。
> 对应章节：ch09（Reporter 解耦 + 飞书长连接）。

---

## 0. 前置概念：三个名词

| 名词 | 是什么 | 在你的项目里 |
|---|---|---|
| **自建应用**（Custom App） | 建在某个“企业/组织”下的应用，仅该组织内可用 | `测试智能体LITTLE-ROBOT` |
| **企业/组织**（tenant） | 飞书里的一级组织，个人也能免费创建 | `ddcompany` |
| **长连接**（WebSocket） | 你的进程主动连飞书，飞书把事件推过来 | 不需要公网 IP / 域名 / 内网穿透 |

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

### 1.4 开权限（**4 条，缺一不可**）

「权限管理」→「开通权限」→ 按**中文名称**搜索（按 code 搜反而不准）→ 勾选 →「确认开通权限」。

| # | 权限 code | 名称 | 不开会怎样 |
|---|---|---|---|
| 1 | `im:message` | 获取与发送单聊、群组消息 | 收发全不通 |
| 2 | `im:message.p2p_msg:readonly` | **读取用户发给机器人的单聊消息** | **单聊没有输入框、收不到消息** ← 最容易漏 |
| 3 | `im:message.group_at_msg:include_bot:readonly` | 获取群组中其他机器人和用户@当前机器人的消息 | 群里 @ 也收不到 |
| 4 | `im:message:send_as_bot` | 以应用的身份发消息 | 能收不能回 |

> **为什么第 2 条容易漏**：添加「接收消息」事件时，飞书只会自动帮你补第 3 条（群内 @）。
> 单聊需要的 `p2p_msg:readonly` 不会自动加，而缺了它，**单聊窗口连对话框都不给**。
> 官方 FAQ 明确写了这一条：<https://open.feishu.cn/document/faq/bot.md>

### 1.5 事件订阅（必须点保存）

- [ ] 「事件与回调」→ 事件配置 → 订阅方式选 **使用长连接接收事件**
- [ ] **点「保存」**（不保存，下面的「添加事件」按钮永远是灰的）
- [ ] 「添加事件」→ 搜「接收消息」→ 勾 `im.message.receive_v1` → 添加
- [ ] 弹窗提示「推荐开通以下权限」→ 点**确认开通权限**

### 1.6 发布版本（不发布 = 前面全白配）

- [ ] 「版本管理与发布」→「创建版本」→ 版本号 `1.0.0` → 更新说明随便填
- [ ] **保存** → **确认发布**（自建应用免审核，你自己就是审批人）

> ⚠️ **铁律：每次改权限 / 改事件 / 改机器人配置，都要重新发一版。**
> 这是「明明配了却不生效」的头号原因，占所有排查的一半。

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

- [ ] **单聊**：工作台 → 点应用图标 → 直接进单聊（任何文本都能收到）
- [ ] **群聊**：群里加机器人 → **必须 @ 它** 才会推事件
- [ ] 单聊不需要 @；群聊必须 @

---

## 2. 代码结构（三块）

| 文件 | 职责 | 依赖方向 |
|---|---|---|
| `internal/engine/reporter.go` | 定义 `Reporter` 接口：引擎只发事件，不管展示 | engine 不依赖任何外壳 |
| `internal/feishu/reporter.go` | 实现 `Reporter`，把事件变成飞书消息 | feishu → engine |
| `internal/feishu/bot.go` | 收事件 → 去重 → 会话串行 → 后台跑 `eng.Run` | feishu → engine |
| `cmd/bot/main.go` | 组合根：读 env、装配 engine / registry、启动长连接 | 同时认识两边 |

### 四个必须处理的工程点

1. **事件去重**：飞书没收到成功响应会重推，按 `message_id` 去重，否则模型会把同一件事做两遍；
2. **handler 立刻返回**：只在 handler 里登记 + 起 goroutine，不要同步跑 Agent（长连接 SDK 是串行处理的，堵住会积压）；
3. **同一会话串行**：每个 `chat_id` 一把 `sync.Mutex`，忙时回「稍后再发」，避免两个任务同时改同一个工作区；
4. **不继承 handler 的 ctx**：goroutine 里用 `context.Background()` + 自己的超时，否则 handler 一返回 ctx 被取消，任务半路崩。

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

## 4. 七个坑

| 现象 | 原因 | 处理 |
|---|---|---|
| 单聊没输入框 / 收不到 | 缺 `im:message.p2p_msg:readonly` | 补权限 + **重新发版** |
| 群里 @ 才理我 | 群聊规则如此；要收全量需 `im:message.group_msg`（敏感） | 用单聊，或申请该权限 |
| 配了权限还是不回 | 没重新发布版本 | 每次改配置都发版 |
| 有 `[Feishu] 收到消息` 但不回 | 缺 `send_as_bot`；或 `content` 不是合法 JSON | 看有没有 `[Feishu] 回复失败：...` |
| 消息时有时无 | 同时起了两个 bot 进程，抢同一条长连接 | 只留一个进程 |
| 事件日志「暂无查询结果」 | 飞书没推 | 回到权限 / 事件 / 版本三项 |
| 改了 `.env` 不生效 | 环境变量在进程启动时读入，`os.Getenv` 不重读文件 | **重启进程** |

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
| 卡片按钮审批 | 高危 `bash` 前弹「✅ 执行 / ❌ 取消」 | ch16 |
| 主动推送 | `Im.Message.Create` + 定时器，定时汇报 | — |
| 会话记忆 | 按 `chat_id` 存 `Session`，否则每条消息都是新对话 | ch11 |
| 上下文压缩 | 有记忆后必须压缩，否则 token 爆炸 | ch12 |
| 图片/文件消息 | 处理 `message_type` 为 `image`/`file` 的事件 | — |
