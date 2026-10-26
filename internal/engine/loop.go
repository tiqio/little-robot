package engine

import (
	"context"
	"errors"
	"fmt"
	"little-robot/internal/observability"
	"little-robot/internal/prompt"
	"little-robot/internal/provider"
	"little-robot/internal/runstate"
	"little-robot/internal/schema"
	"little-robot/internal/session"
	"little-robot/internal/tools"
	"log"
	"path/filepath"
	"strings"
	"sync"
)

const maxTurns = 300

// maxEmptyNudges：模型"没说话也没调工具"时最多救援几次。
// 思考型模型会把输出额度全花在 reasoning 上，finish_reason=length 时 content 是空的；
// 之前这种情况被当成"任务完成"直接收尾，用户在飞书看到一片空白。
const maxEmptyNudges = 2

// silentTurn 判断这一轮是不是"空输出"（没说话也没调工具）
func silentTurn(resp *schema.Message) bool {
	return len(resp.ToolCalls) == 0 && strings.TrimSpace(resp.Content) == ""
}

// emptyNudge 救援文案：把"该怎么改"直接写给模型（和 hintFor 同一个套路）
func emptyNudge(finishReason string) string {
	nudge := "（系统救援）你上一轮没有可见输出、也没调工具"
	if finishReason == "length" {
		nudge += "（思考把输出额度用光了）"
	}
	return nudge + "。这一轮把思考收短（100字以内），然后直接调一个工具或给结论，别重复上一轮的纠结。"
}

// silentFallback 两次救援后还是没话：给用户一句话，别让人干等空白
const silentFallback = "（我想了很久没想出结果。换个问法再试试？或者直接告诉我你要哪个。）"

// callModel 发一次生成请求，遇到"后端不接受图片"就剥掉图片重试一次。
//
// 为什么要有这一层：真实事故里一张小 webp 让 llama.cpp 回 400，
// 整个 Run 直接 error 结束，用户什么也没拿到。图片是"锦上添花"，
// 不该成为整个任务的单点故障——剥掉图还能靠文字和工具继续干。
//
// 返回的 noImages 会带到后续轮次：一旦知道这个后端吃不了图，
// 本 Run 就不再尝试，避免每轮都白跑一次请求。
func (e *AgentEngine) callModel(ctx context.Context, history []schema.Message, toolDefs []schema.ToolDefinition, noImages bool) (*schema.Message, []schema.Message, bool, error) {
	if noImages {
		history, _ = stripImages(history)
	}

	resp, err := e.provider.Generate(ctx, history, toolDefs)
	if err == nil {
		return resp, history, noImages, err
	}

	// 上下文超限：把历史狠压一遍（连图一起剥）再发一次。
	// 自动压缩按 65% 阈值压、保护区还留 36 条，撞线时往往压得不够狠。
	if errors.Is(err, provider.ErrContextOverflow) {
		budget := e.contextTokens * 2 / 10
		squeezed, _ := e.compactor.SqueezeTo(history, budget) // 急救：压到预算内，实在不行折叠成摘要
		before := e.compactor.Estimate(history)
		after := e.compactor.Estimate(squeezed)
		log.Printf("[Engine] 上下文超限，压缩后重试：%d -> %d 字符", before, after)
		if after < before {
			resp2, err2 := e.provider.Generate(ctx, squeezed, toolDefs)
			return resp2, squeezed, true, err2
		}
		return nil, history, noImages, err
	}

	if !errors.Is(err, provider.ErrImageRejected) {
		return resp, history, noImages, err
	}

	stripped, ok := stripImages(history)
	if !ok {
		return nil, history, noImages, err // 本来就没图，救不了
	}
	log.Printf("[Engine] 后端拒绝了图片，剥掉图片重试一次（本 Run 后续不再带图）：%v", err)
	resp, err2 := e.provider.Generate(ctx, stripped, toolDefs)
	return resp, stripped, true, err2
}

// stripImages 把所有消息里的图片换成一句"看过图"的占位；返回 false 表示本来就没图。
// 和 compactor/store 用同一句话，模型在三个地方看到的占位一致。
func stripImages(msgs []schema.Message) ([]schema.Message, bool) {
	found := false
	out := make([]schema.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		if len(out[i].Images) == 0 {
			continue
		}
		found = true
		n := len(out[i].Images)
		out[i].Images = nil
		note := session.ImageNote(n)
		if strings.TrimSpace(out[i].Content) == "" {
			out[i].Content = note
		} else {
			out[i].Content += "\n" + note
		}
	}
	return out, found
}

/*
	如何把call循环变成并发，需要注意哪些地方？
	1. history不能在goroutine里面append：这样会竞争，因此需要收集到一个局部切片，WaitGroup等齐了，再一次性append进history
	2. 结果顺序必须和请求顺序一致：模型发的是[call_0, call_1, call_2]，对应三个tool_call_id，虽然靠ToolCallID认领，但把结果按照原顺序放回去更安全，很多后端要求tool消息在assistant消息后面，而且顺序一致，goroutine(idx=1)只写observationMsgs[1]
	3. 循环变量要显式传给goroutine：for _, call := range ...里，call是共享的同一个变量，goroutine启动时它可能已经变成下一次的值了，Go 1.22起每个迭代有独立变量，但是这样永远不会有歧义
*/

// 结构体
type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	WorkDir        string
	EnableThinking bool // 注意这里是大写的，因为外部可以读写
	PlanMode       bool
	// thinkTokens/actTokens 思考段/行动段的输出预算（ch33/S2）。
	// 0 = 不覆盖，用 provider 的默认 max_tokens。两段分开，思考吃不到行动的份。
	thinkTokens int
	actTokens   int
	// contextTokens 模型窗口（SetContextTokens 时记下来），用来做"贴窗口"的硬保护
	contextTokens int
	composer      *prompt.Composer
	compactor     *session.Compactor
	reminder      *Reminder
}

// 构造函数
// charsPerToken 是"字符数→token"的保守换算：
// 中文约 1 字/token，英文约 4 字符/token，取 2 留余量（宁可早压，也别等报超窗）。
const charsPerToken = 2

// contextFillRatio 历史占到窗口的这个比例就开始压缩。
// 从 0.65 降到 0.15 的原因：本地模型**预填充只有 65~130 token/s**，
// prompt 大小直接等于等待时长——
//
//	8K token ≈ 1~2 分钟；25K ≈ 4 分钟；100K ≈ 20 分钟。
//
// 65% 意味着要等十几分钟才"开始压"，用户看到的就是"卡住不动"。
// 15% 让 prompt 稳定在 2 万 token 以内，响应时间可控。
const contextFillRatio = 0.15

func defaultMaxChars(contextTokens int) int {
	if contextTokens <= 0 {
		contextTokens = 128 * 1024
	}
	return int(float64(contextTokens) * contextFillRatio * charsPerToken)
}

// SetContextTokens 按模型上下文窗口重设压缩阈值。
// 窗口越大能留的原话越多；本地模型窗口小（或跑不动大窗口）时必须调小。
func (e *AgentEngine) SetContextTokens(contextTokens int) {
	keep := 36
	if e.compactor != nil {
		keep = e.compactor.RetainLast
	}
	e.compactor = session.NewCompactor(defaultMaxChars(contextTokens), keep)
	e.contextTokens = contextTokens
	log.Printf("[Engine] 上下文窗口 %d tokens，压缩阈值 %d 字符", contextTokens, e.compactor.MaxChars)
}

// SetThinkingBudgets 给思考段/行动段分别设输出预算（ch33/S2）。
// 0 = 该段不覆盖 provider 默认。只有 EnableThinking=true 时思考段预算才用得上。
func (e *AgentEngine) SetThinkingBudgets(thinkTokens, actTokens int) {
	e.thinkTokens = thinkTokens
	e.actTokens = actTokens
	log.Printf("[Engine] 思考/行动预算：%d/%d tokens（0=用 provider 默认）", thinkTokens, actTokens)
}

// tailRunes 取字符串末尾 n 个 rune（按字切，不切半个中文）
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking,
		PlanMode:       false,
		composer:       prompt.NewComposer(workDir),
		compactor:      session.NewCompactor(defaultMaxChars(128*1024), 36),
		reminder:       NewReminder(),
	}
}

func (e *AgentEngine) Run(ctx context.Context, s *session.Session, userPrompt string, reporter Reporter) error {
	if s == nil {
		return fmt.Errorf("session不能为nil")
	}
	if reporter == nil {
		reporter = NewTerminalReporter()
	}

	// 这样每个Run都会给自己挂一个RunState，之后只需要调整Dir即可
	//rs := runstate.New()
	//ctx = runstate.With(ctx, rs) 这样不对，因为宿主在外面设置的东西会被冲掉
	rs := runstate.From(ctx)
	if rs == nil { // 当已经存在worktree/存在RunState的时候就不进行动作
		rs = runstate.New()
		ctx = runstate.With(ctx, rs)
	}

	// 开根Span，退出时落盘
	ctx, root := observability.StartSpan(ctx, "run") // root挂进ctx，同时ctx被替换，一路传到registry.Execute，从ctx里面取得root，append成它的孩子
	root.Set("session", s.ID)
	root.Set("workdir", e.WorkDir)
	root.Set("chat", rs.ChatName)
	root.Set("prompt_len", len(userPrompt))
	defer func() {
		root.End()
		traceDir := e.WorkDir
		if rs.WorkDir != "" {
			traceDir = rs.WorkDir // 优先跟着群走
		}
		//path, err := root.Save(filepath.Join(e.WorkDir, ".claw", "traces"))
		path, err := root.Save(filepath.Join(traceDir, ".claw", "traces"))
		if err != nil {
			log.Printf("[Trace] 落盘失败：%v", err)
			return
		}
		log.Printf("[Trace] 已保存：%s", path)
	}()

	log.Printf("[Engine] 会话 %s | 工作区 %s | 已有历史 %d 条", s.ID, e.WorkDir, s.Len())

	// 用户这轮说的话给会话
	s.Append(schema.Message{Role: schema.RoleUser, Content: userPrompt})

	// 每个 Run 只规划一次：用户消息触发思考，之后的工具轮直接行动。
	// 每轮都规划 = 每次工具结果回来都重新想一遍，27B 上纯属烧 token
	planned := false
	noImages := false // 后端拒过一次图之后，本 Run 不再带图
	turn := 0
	emptyNudges := 0
	for {
		turn++
		if turn > maxTurns {
			return fmt.Errorf("超过最大轮数%d，已终止", maxTurns)
		}
		log.Printf("===== [Turn %d] 开始 ====\n", turn)

		availableTools := e.registry.GetAvailableTools()

		// system每轮预算：改了AGENTS.md / 加了技能，下一轮就生效
		history := append([]schema.Message{e.composer.Build(e.PlanMode)}, s.Messages()...) // 前一秒加进去的userPrompt现在在这里被返回

		// 发送请求前压缩：只换内容，不删除消息
		history = e.compactor.Compact(history) // 位置很关键：必须在Generate之前，s.Append之后，压的是“发出去的快照”，不是会话本身

		// 硬保护：估算已经超过 prompt 预算，说明常规压缩不够，直接按预算狠压。
		// 与其让后端回 400 再补救，不如发之前就压好（少一次失败、少一次等待）
		if e.contextTokens > 0 && e.compactor.Estimate(history)/charsPerToken > e.contextTokens*2/10 {
			before := e.compactor.Estimate(history)
			// 预算 = 窗口的 10%（字符 ≈ token×2）：13 万 token 的窗口压到 2.6 万字符，
			// 本地 65 tok/s 的预填充约 3.5 分钟，还在可接受范围。
			budget := e.contextTokens * 2 / 10
			var folded bool
			history, folded = e.compactor.SqueezeTo(history, budget)
			log.Printf("[Engine] 上下文逼近窗口（%d 字符 ≈ %d token / 窗口 %d token），压到 %d 字符（折叠=%v）",
				before, before/charsPerToken, e.contextTokens, e.compactor.Estimate(history), folded)
			// 诊断：说清楚"大块头"到底是谁，否则永远不知道 30 万字符从哪来
			log.Printf("[Engine] 最大消息：%s", strings.Join(session.TopMessages(history, 3), " | "))
		}

		var thinking string

		if e.EnableThinking && !planned {
			planned = true
			log.Println("[PLAN] 收走工具，进入规划阶段（本Run只规划一次）...")
			reporter.OnThinkingStart(ctx)
			// 思考段用独立预算（thinkTokens）：想再长也吃不到行动段的份
			thinkResp, newHistory, nmi, err := e.callModel(provider.WithMaxTokens(ctx, e.thinkTokens), history, nil, noImages)
			history, noImages = newHistory, nmi
			if err != nil {
				return fmt.Errorf("思考阶段生成失败：%w", err)
			}
			thinking = strings.TrimSpace(thinkResp.Content)
			if thinking == "" {
				// 思考型模型：content 是空的，计划全在 reasoning_content 里。
				// 取思考的末尾当"计划"——决策都在最后，开头那些"嗯...可能是..."是废料
				if r := strings.TrimSpace(thinkResp.Reasoning); r != "" {
					thinking = "（思考摘要）" + tailRunes(r, 300)
					log.Printf("[Engine] 思考段 content 为空，用 reasoning 末尾 %d 字当计划", 300)
				}
			}
			if thinking != "" {
				reporter.OnThinkingText(ctx, thinking)
				// 只把"计划"喂给行动段，不喂整段原始思考（省 token）；
				// history 是发给模型的快照，session 是真实存档，两者分开维护
				history = append(history, schema.Message{Role: schema.RoleAssistant, Content: thinking})
			}
		}

		//log.Println("[EXECUTE] 进入执行阶段...")
		// 行动段用独立预算（actTokens）：只需要吐工具调用或短结论
		resp, newHistory, nmi, err := e.callModel(provider.WithMaxTokens(ctx, e.actTokens), history, availableTools, noImages)
		history, noImages = newHistory, nmi
		if err != nil {
			return fmt.Errorf("模型生成失败：%w", err)
		}

		// s.Append(*resp)

		// 关键：整个一轮只落一条assistant消息，OpenAI兼容协议要求角色交替（user——assistant——user——assistant），连续两个assistant，部分后端直接400
		finalMsg := schema.Message{
			Role:      schema.RoleAssistant,
			Content:   strings.TrimSpace(thinking + "\n" + resp.Content),
			ToolCalls: resp.ToolCalls,
		}

		s.Append(finalMsg)

		if resp.Content != "" {
			reporter.OnAssistantMessage(ctx, resp.Content)
		}

		if len(resp.ToolCalls) == 0 {
			// 空输出救援：模型只思考没输出（length=额度被思考吃光 / stop=自己沉默），
			// 不能直接当"任务完成"收尾，否则用户端一片空白
			if silentTurn(resp) && emptyNudges < maxEmptyNudges {
				emptyNudges++
				log.Printf("[Engine] 空输出救援 %d/%d（finish_reason=%s）", emptyNudges, maxEmptyNudges, resp.FinishReason)
				s.Append(schema.Message{Role: schema.RoleUser, Content: emptyNudge(resp.FinishReason)})
				continue
			}
			if silentTurn(resp) {
				reporter.OnAssistantMessage(ctx, silentFallback)
			}
			log.Printf("[Engine] 模型不再请求工具，任务完成，推出循环")
			break
		}

		log.Printf("[Engine] 模型请求调用 %d 个工具：", len(resp.ToolCalls))
		for i, c := range resp.ToolCalls {
			log.Printf("  [%d] %s  %s", i, c.Name, previewArgs(c.Arguments, 160))
		}

		observationMsgs := make([]schema.Message, len(resp.ToolCalls))
		// 需要保留一下这个results来看是不是会原地打转
		results := make([]schema.ToolResult, len(resp.ToolCalls))
		var wg sync.WaitGroup

		for i, call := range resp.ToolCalls {
			wg.Add(1)
			go func(idx int, c schema.ToolCall) {
				defer wg.Done()
				reporter.OnToolCall(ctx, c)
				result := e.registry.Execute(ctx, c)
				reporter.OnToolResult(ctx, c, result)

				results[idx] = result
				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: c.ID,
				}
			}(i, call)
		}
		wg.Wait()

		s.Append(observationMsgs...)

		// 工具产出的图片作为一条额外的 user 消息注入：厂商只允许 user 消息带图，
		// 塞进 tool 消息会 400。模型看到图才能"先确认再发送"。
		for _, r := range results {
			if len(r.Images) == 0 {
				continue
			}
			s.Append(schema.Message{
				Role:    schema.RoleUser,
				Content: "（系统注入）这是工具刚刚拿到的图片，请先看图判断是否符合要求，再决定发送还是换下一张。",
				Images:  r.Images,
			})
		}

		nudge, abort := e.reminder.Observe(resp.ToolCalls, results)
		if abort {
			// 同一个调用重复到上限：与其烧到 300 轮，不如停下来把选择权交回用户
			log.Printf("[Engine] 检测到原地打转（连续 %d 次相同调用），终止本 Run", e.reminder.Streak())
			msg := AbortMessage(lastToolName(resp.ToolCalls), e.reminder.Streak())
			reporter.OnAssistantMessage(ctx, msg)
			s.Append(schema.Message{Role: schema.RoleAssistant, Content: msg})
			return nil
		}
		if nudge != nil {
			s.Append(*nudge)
		}
	}

	return nil
}

// lastToolName 打转终止时告诉用户"卡在哪个工具上"
func lastToolName(calls []schema.ToolCall) string {
	if len(calls) == 0 {
		return "工具"
	}
	return calls[len(calls)-1].Name
}

// 为什么要engine自己NewComposer而不是从外层注入？因为这样构造函数少一个参数，代价是engine和文件系统耦合了——要单测Run就得准备真实目录
// RunOnce全程没有Trace，所以不需要更改，自己维护history
func (e *AgentEngine) RunOnce(ctx context.Context, userPrompt string, reporter Reporter) error {
	// 一处都低，后面就不用每个回调都判空；漏判一处就是panic
	if reporter == nil {
		reporter = NewTerminalReporter()
	}

	log.Printf("[Engine] 引擎启动，工作区：%s\n", e.WorkDir)

	// 引擎现在是有状态的：后果是一次Run结束，历史就没有了，飞书上用户说下一个话，模型不知道之前聊过什么
	/*
		因此需要把历史从引擎里搬出去，放进Session：
			1. 引擎：无状态（同时可以服务100个会话）
			2. Session：有状态（每个会话自己的历史）
		多轮会话，多用户隔离，断点回复（ch14），上下文压缩（ch12）全部建立在历史有个明确的归属上面

		三个关键设计：
			1. system消息不进Session：每一轮现算composer.Build()，这样改了AGENTS.md后下一轮会立刻生效
			2. Messages()必须返回副本：Go的切片是引用，如果直接return s.history，调用方一个append就可能改会话内部
			3. 引擎改成无状态，才能并发：引擎里面不能有任何当前会话的字段——WorkDir、provider、registry都是只读的，历史全在Session里面
	*/
	history := []schema.Message{
		// {
		// 	Role: schema.RoleSystem,
		// 	// 这里的system prompt一个字都没有说什么情况下用哪个工具，所以相当于把4个工具告诉它随便用，解决方法是换成结构化的一段，或者在工具的Description里面进行限制
		// 	Content: "You are little-robot, an expert coding assistant. You have full access to tools in the workspace",
		// },
		e.composer.Build(e.PlanMode),
		{
			Role:    schema.RoleUser,
			Content: userPrompt,
		},
	}

	turn := 0
	emptyNudges := 0
	for {
		turn++
		log.Printf("===== [Turn %d] 开始 =====\n", turn)

		availableTools := e.registry.GetAvailableTools()

		// ===== 先做计划不让它跑工具 =====
		if e.EnableThinking {
			log.Println("[Phase 1] 收走工具，进入规划阶段...")
			reporter.OnThinkingStart(ctx)
			thinkResp, err := e.provider.Generate(provider.WithMaxTokens(ctx, e.thinkTokens), history, nil) // 传入nil就是收走工具，能用参数约束的就不要用提示词求它
			if err != nil {
				return fmt.Errorf("思考阶段生成失败：%w", err)
			}
			if thinkResp.Content != "" { // 没有append的时候才跳过，因为不少OpenAI的兼容后端会拒绝{"role":"assistant","content":""}(400)
				//fmt.Printf("[内部思考]：%s\n", thinkResp.Content)
				reporter.OnThinkingText(ctx, thinkResp.Content)
				history = append(history, *thinkResp) // 支持思考的话要让这个AI看到思考过程
			}
		}

		// ===== 挂回工具，让它行动 =====
		log.Println("[Phase 2] 恢复工具，等待行动...")
		resp, err := e.provider.Generate(provider.WithMaxTokens(ctx, e.actTokens), history, availableTools)
		if err != nil {
			return fmt.Errorf("模型生成失败：%w", err)
		}

		history = append(history, *resp) // 模型的话也要进入历史

		if resp.Content != "" {
			//fmt.Printf("模型：%s\n", resp.Content)
			reporter.OnAssistantMessage(ctx, resp.Content)
		}

		if len(resp.ToolCalls) == 0 {
			// 与 Run 同一套空输出救援
			if silentTurn(resp) && emptyNudges < maxEmptyNudges {
				emptyNudges++
				log.Printf("[Engine] 空输出救援 %d/%d（finish_reason=%s）", emptyNudges, maxEmptyNudges, resp.FinishReason)
				history = append(history, schema.Message{Role: schema.RoleUser, Content: emptyNudge(resp.FinishReason)})
				continue
			}
			if silentTurn(resp) {
				reporter.OnAssistantMessage(ctx, silentFallback)
			}
			log.Println("[Engine] 模型不再请求工具，任务完成，退出循环")
			break
		}

		/*
			log.Printf("[Engine] 模型请求调用%d个工具...\n", len(resp.ToolCalls))

			for _, call := range resp.ToolCalls {
				log.Printf("	-> 执行工具：%s，参数：%s\n", call.Name, string(call.Arguments))

				result := e.registry.Execute(ctx, call)

				if result.IsError {
					log.Printf("	-> 工具执行报错：%s\n", result.Output)
				} else {
					log.Printf("	-> 工具执行成功（返回%d字节）\n", len(result.Output))
				}

				history = append(history, schema.Message{ // 工具结果伪装成user
					Role:       schema.RoleUser, // 靠ToolCallID认领
					Content:    result.Output,
					ToolCallID: call.ID, // 这条ID是回填的证据
				}) // 注释掉这个mock不变，因为mock靠计数器，不靠历史，而且当mock永远返回ToolCalls后会死循环刷屏，这就是加护栏的原因
			}
		*/

		log.Printf("[Engine] 模型请求并发调用%d个工具...\n", len(resp.ToolCalls))

		// 预分配：每个goroutine写自己的下标，天然保序且不需要加锁
		observationMsgs := make([]schema.Message, len(resp.ToolCalls))
		var wg sync.WaitGroup

		for i, call := range resp.ToolCalls {
			wg.Add(1)

			// i和call显式传参，避免闭包捕获循环变量带来的歧义
			go func(idx int, c schema.ToolCall) {
				defer wg.Done()

				//log.Printf("\t-> [%d] 执行工具：%s，参数：%s\n", idx, c.Name, string(c.Arguments))
				reporter.OnToolCall(ctx, c)
				result := e.registry.Execute(ctx, c)

				//if result.IsError {
				//	log.Printf("\t-> [%d] 工具执行报错：%s\n", idx, result.Output)
				//} else {
				//	log.Printf("\t-> [%d] 工具执行成功（返回%d字节）\n", idx, len(result.Output))
				//}
				reporter.OnToolResult(ctx, c, result)

				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: c.ID,
				}
			}(i, call)
		}

		wg.Wait() // 等待所有工具跑完

		// 按照顺序追加回历史，保证tool消息的次序和tool_calls一致
		history = append(history, observationMsgs...)
	}

	return nil
}

// previewArgs 把一行 JSON 压成单行并截断，避免把 write_file 的整篇内容打到终端
func previewArgs(raw []byte, max int) string {
	s := strings.Join(strings.Fields(string(raw)), " ") // 压缩换行和多余空格
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf("…（共%d字节）", len(s))
}

// CompactSession 手动压缩：把会话历史换成压缩后的版本，返回压缩前后字符数。
// 飞书"压缩上下文"按钮走这里——外壳不该知道 compactor 长什么样。
func (e *AgentEngine) CompactSession(s *session.Session) (before, after int) {
	if s == nil || e.compactor == nil {
		return 0, 0
	}
	msgs := s.Messages()
	before = e.compactor.Estimate(msgs)
	out := e.compactor.ManualCompact(msgs)
	after = e.compactor.Estimate(out)
	s.Replace(out)
	log.Printf("[Engine] 手动压缩会话 %s：%d -> %d 字符", s.ID, before, after)
	return before, after
}

// SessionChars 会话历史当前多大（字符数近似），按钮回执用
func (e *AgentEngine) SessionChars(s *session.Session) int {
	if s == nil || e.compactor == nil {
		return 0
	}
	return e.compactor.Estimate(s.Messages())
}

// Pressure 会话相对模型窗口的占用比（0~1）：外壳拿它决定"要不要提醒用户压缩"。
//
// 为什么挂在引擎上而不是记住"这次 Run 压过没"：
// 一个引擎同时服务多个群，把"本轮状态"存在引擎字段里会互相干扰；
// 而会话本身是现成的、每群独立的，直接按当前历史算最准确。
func (e *AgentEngine) Pressure(s *session.Session) (chars int, ratio float64) {
	if s == nil || e.compactor == nil || e.contextTokens <= 0 {
		return 0, 0
	}
	chars = e.compactor.Estimate(s.Messages())
	return chars, float64(chars) / float64(e.contextTokens*charsPerToken)
}
