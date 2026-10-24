package engine

import (
	"context"
	"fmt"
	"little-robot/internal/observability"
	"little-robot/internal/prompt"
	"little-robot/internal/provider"
	"little-robot/internal/schema"
	"little-robot/internal/session"
	"little-robot/internal/tools"
	"log"
	"path/filepath"
	"strings"
	"sync"
)

const maxTurns = 300

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
	composer       *prompt.Composer
	compactor      *session.Compactor
	reminder       *Reminder
}

// 构造函数
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking,
		PlanMode:       false,
		composer:       prompt.NewComposer(workDir),
		compactor:      session.NewCompactor(200000, 36),
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

	// 开根Span，退出时落盘
	ctx, root := observability.StartSpan(ctx, "run") // root挂进ctx，同时ctx被替换，一路传到registry.Execute，从ctx里面取得root，append成它的孩子
	root.Set("session", s.ID)
	root.Set("workdir", e.WorkDir)
	root.Set("prompt_len", len(userPrompt))
	defer func() {
		root.End()
		path, err := root.Save(filepath.Join(e.WorkDir, ".claw", "traces"))
		if err != nil {
			log.Printf("[Trace] 落盘失败：%v", err)
			return
		}
		log.Printf("[Trace] 已保存：%s", path)
	}()

	log.Printf("[Engine] 会话 %s | 工作区 %s | 已有历史 %d 条", s.ID, e.WorkDir, s.Len())

	// 用户这轮说的话给会话
	s.Append(schema.Message{Role: schema.RoleUser, Content: userPrompt})

	turn := 0
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

		var thinking string

		if e.EnableThinking {
			log.Println("[PLAN] 收走工具，进入规划阶段...")
			reporter.OnThinkingStart(ctx)
			thinkResp, err := e.provider.Generate(ctx, history, nil)
			if err != nil {
				return fmt.Errorf("思考阶段生成失败：%w", err)
			}
			if thinkResp.Content != "" {
				reporter.OnThinkingText(ctx, thinkResp.Content)
				thinking = thinkResp.Content
				// s.Append(*thinkResp) // 注意这里同时维护了两个副本，session和history，history是为了包含systemPrompt和toolPrompt的：history是发给模型的快照，s是真实存档，让Phase2看到思考
				history = append(history, *thinkResp)
			}
		}

		//log.Println("[EXECUTE] 进入执行阶段...")
		resp, err := e.provider.Generate(ctx, history, availableTools)
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

		if nudge := e.reminder.Observe(resp.ToolCalls, results); nudge != nil {
			s.Append(*nudge)
		}
	}

	return nil
}

// 为什么要engine自己NewComposer而不是从外层注入？因为这样构造函数少一个参数，代价是engine和文件系统耦合了——要单测Run就得准备真实目录
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
	for {
		turn++
		log.Printf("===== [Turn %d] 开始 =====\n", turn)

		availableTools := e.registry.GetAvailableTools()

		// ===== 先做计划不让它跑工具 =====
		if e.EnableThinking {
			log.Println("[Phase 1] 收走工具，进入规划阶段...")
			reporter.OnThinkingStart(ctx)
			thinkResp, err := e.provider.Generate(ctx, history, nil) // 传入nil就是收走工具，能用参数约束的就不要用提示词求它
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
		resp, err := e.provider.Generate(ctx, history, availableTools)
		if err != nil {
			return fmt.Errorf("模型生成失败：%w", err)
		}

		history = append(history, *resp) // 模型的话也要进入历史

		if resp.Content != "" {
			//fmt.Printf("模型：%s\n", resp.Content)
			reporter.OnAssistantMessage(ctx, resp.Content)
		}

		if len(resp.ToolCalls) == 0 {
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
