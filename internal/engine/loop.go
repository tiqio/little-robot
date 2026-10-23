package engine

import (
	"context"
	"fmt"
	"little-robot/internal/prompt"
	"little-robot/internal/provider"
	"little-robot/internal/schema"
	"little-robot/internal/tools"
	"log"
	"sync"
)

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
	composer       *prompt.Composer
}

// 构造函数
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking,
		composer:       prompt.NewComposer(workDir),
	}
}

// 为什么要engine自己NewComposer而不是从外层注入？因为这样构造函数少一个参数，代价是engine和文件系统耦合了——要单测Run就得准备真实目录
func (e *AgentEngine) Run(ctx context.Context, userPrompt string, reporter Reporter) error {
	// 一处都低，后面就不用每个回调都判空；漏判一处就是panic
	if reporter == nil {
		reporter = NewTerminalReporter()
	}

	log.Printf("[Engine] 引擎启动，工作区：%s\n", e.WorkDir)

	history := []schema.Message{
		// {
		// 	Role: schema.RoleSystem,
		// 	// 这里的system prompt一个字都没有说什么情况下用哪个工具，所以相当于把4个工具告诉它随便用，解决方法是换成结构化的一段，或者在工具的Description里面进行限制
		// 	Content: "You are little-robot, an expert coding assistant. You have full access to tools in the workspace",
		// },
		e.composer.Build(),
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
