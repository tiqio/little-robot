# LLM 输出机制：为什么之前会"静默退出"，现在怎么治

> 对应 ch32/ch33/ch34。起因：飞书让抓"型月伊莉雅"的本子，bot 跑了 74 秒、一个
> 工具没调、一个字没回，直接结束。日志显示 `finish_reason:"length"`、
> `reasoning_tokens:1024`、`content:""`——**思考把输出额度全吃光了**。

## 1. 原来的机制（为什么会截断）

```
一次 completion = 思考(reasoning_content) + 行动(content/tool_calls)
共用一个 max_tokens（本地写死 1024）
```

- 思考型模型（Qwen3 系）的 `reasoning_content` 和正式输出**算同一笔账**；
- 27B 量化版想"伊莉雅到底是谁"想了 1024 token → `content:""`、`tool_calls:[]`、`finish_reason:"length"`；
- 引擎把"没调工具"当"做完了" → `break`，`content` 又是空的 → 飞书端 `OnAssistantMessage`
  一次没触发 → **静默收尾**。

`max_tokens` 调大只是把悬崖往后挪；真正的修法是**摘要进、查找出**：跨窗口的只留摘要，
完整素材放外面，思考和行动各占各的预算。

## 2. S1 止损（ch32）：截断不再静默

| 改动 | 位置 | 作用 |
|---|---|---|
| `FinishReason` 字段 | `internal/schema/message.go` | provider 把 `finish_reason` 透传出来（之前直接丢弃） |
| 本地默认 2048 | `NewLMStudioProvider` | `LOCAL_MAX_TOKENS` 可覆盖 |
| 空输出救援 | `engine/loop.go` `silentTurn/emptyNudge` | 空 content + 没工具 → 注入一条"系统救援"user 消息再来一轮（最多 2 次），`finish_reason=length` 时多说一句"思考把额度用光了"；两次都空 → 给用户一句兜底话，**永远不静默** |

## 3. S2 思考/行动分家（ch33）：预算隔离

```
Run 开始 ──► 思考段：收走工具、ThinkTokens 预算（默认 2048）──► 计划
                └─► 行动段：挂回工具、ActTokens 预算（默认 1024）──► 工具调用/短结论
                └─► 之后每轮只有行动段（每个 Run 只规划一次，省调用）
```

- **每个 Run 只思考一次**：用户消息触发规划，工具结果回来直接行动（之前每轮都重新想一遍）；
- **两段预算独立**：通过 `provider.WithMaxTokens(ctx, n)` 覆盖，`LLMProvider` 接口零改动，
  CostTracker/mock 全不用跟着改；
- **reasoning_content 透传**：`schema.Message.Reasoning`；思考段 content 为空时取
  reasoning **末尾 300 字**当计划（决策都在最后，开头的"嗯…可能是…"是废料）；
- **计划只喂"摘要"**：行动段的历史里是那句计划，不是整段原始思考；
- **飞书回显**：思考段开始发"💭 思考中…"（74 秒无回显的教训）；
- **逃生门**：`LOCAL_NO_THINK=true` → `chat_template_kwargs.enable_thinking=false`，
  彻底关思考，输出又快又稳但不假思索。

开关在 `app/llm.go`：本地默认 `THINKING=on`，云端默认 off（reasoner 模型手动开）。

## 4. S3 摘要（ch34）：进上下文的是 digest

| 改动 | 位置 | 作用 |
|---|---|---|
| 图片降采样 | `tools/image_scale.go` + `fileToDataURL` | >256KB 的图缩到最长边 768px、JPEG q80 再进上下文；原图留盘，`send`/`view_image` 要全尺寸再拉。3MB PNG ≈ 几万 token → 百来 KB |
| 老图剥占位 | `session/compactor.go` | 保护区外的图片消息剥掉 data URL、留"（此处查看过 N 张图片）"；保护区内的保留 |
| 图片计入体积 | `compactor.estimate()` | data URL 长度算进压缩阈值（之前图片完全不计，阈值永不触发） |
| 压缩比 0.75→0.65 | `engine/loop.go` `contextFillRatio` | 多留 10% 窗口给思考段 2048 输出 + 工具结果 |

## 5. 环境变量

```
THINKING=on              # 思考/行动分家（本地默认 on，云端默认 off）
LOCAL_MAX_TOKENS=2048    # 单段（关思考/云端）输出上限
LOCAL_THINK_TOKENS=2048  # 思考段预算
LOCAL_ACT_TOKENS=1024    # 行动段预算
LOCAL_NO_THINK=false     # 彻底关 Qwen3 思考（enable_thinking:false）
```

14 tok/s 本地：典型一轮 ≈ 思考 20-40s + 行动 5-15s；最差 2048+1024 ≈ 4 分钟，
飞书 10 分钟 run 超时兜得住。

## 6. S4 外置查找（待排期）

这次 bug 的根子还有另一半：模型**内部**空转 1000 token 想"伊莉雅=谁"，而
`fetch_image` 早就有"nahida → nahida_(genshin_impact)"的规范标签解析。下一步给它一个
独立的 `lookup`（或 web_search / 角色别名表）：**50 token 查一个事实，而不是 1000 token 硬想**。
