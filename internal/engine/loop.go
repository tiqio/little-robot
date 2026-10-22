package engine

import (
	"context"
	"fmt"
	"little-robot/internal/provider"
	"little-robot/internal/schema"
	"little-robot/internal/tools"
	"log"
)

// 结构体
type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	WorkDir        string
	EnableThinking bool // 注意这里是大写的，因为外部可以读写
}

// 构造函数
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking,
	}
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
	log.Printf("[Engine] 引擎启动，工作区：%s\n", e.WorkDir)

	history := []schema.Message{
		{
			Role:    schema.RoleSystem,
			Content: "You are little-robot, an expert coding assistant. You have full access to tools in the workspace",
		},
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
			log.Println("[Phase 1]收走工具，进入规划阶段...")
			thinkResp, err := e.provider.Generate(ctx, history, nil) // 传入nil就是收走工具，能用参数约束的就不要用提示词求它
			if err != nil {
				return fmt.Errorf("思考阶段生成失败：%w", err)
			}
			if thinkResp.Content != "" { // 没有append的时候才跳过，因为不少OpenAI的兼容后端会拒绝{"role":"assistant","content":""}(400)
				fmt.Printf("[内部思考]：%s\n", thinkResp.Content)
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
			fmt.Printf("模型：%s\n", resp.Content)
		}

		if len(resp.ToolCalls) == 0 {
			log.Println("[Engine] 模型不再请求工具，任务完成，推出循环")
			break
		}

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
	}

	return nil
}
