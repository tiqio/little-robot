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
	provider provider.LLMProvider
	registry tools.Registry
	WorkDir  string
}

// 构造函数
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string) *AgentEngine {
	return &AgentEngine{
		provider: p,
		registry: r,
		WorkDir:  workDir,
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

		log.Println("[Engine] 正在思考（Reasoning）...")
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
