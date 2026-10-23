package engine

import (
	"context"
	"fmt"
	"little-robot/internal/schema"
	"little-robot/internal/tools"
	"log"
	"strings"
	"sync"
	"time"
)

const maxSubTurns = 10

const subAgentPrompt = `你是little-robot的探索子代理，你的唯一职责是：用工具把事实查清楚，交一份精炼报告

纪律：
1. 只相信工具返回的结果，绝不猜测，绝不编造
2. 没找到就继续切换关键词继续找，不要停下来提问——没有人会回答你
3. 找到确切答案后立即停止调用工具，直接输出纯文本报告
4. 报告要短：结论+关键文件路径+行好，不要粘贴大段代码`

// RunSub 注意这个RunSub不碰session，没有PlanMode，没有Reminder，没有reporter —— 子代理保持简单
func (e *AgentEngine) RunSub(ctx context.Context, taskPrompt string, reg tools.Registry) (string, error) {
	history := []schema.Message{
		{
			Role:    schema.RoleSystem,
			Content: subAgentPrompt,
		},
		{
			Role:    schema.RoleUser,
			Content: taskPrompt,
		},
	}

	for turn := 1; ; turn++ {

		log.Printf("[Subagnet] ===== Turn = %d =====", turn)

		if turn > maxSubTurns {
			return "", fmt.Errorf("子代理超过%d轮被召回，请把指令写得更具体", maxSubTurns)
		}

		// 阈值20000~4个中等大小的Go文件，而子代理的本质工作就是一次读一堆文件，所以它注定在第一轮取证后就撞线，在每次出去时都会压缩，压缩是一次性爱管
		// resp, err := e.provider.Generate(ctx, e.compactor.Compact(history), reg.GetAvailableTools())
		turnCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		resp, err := e.provider.Generate(turnCtx, history, reg.GetAvailableTools())
		cancel()
		if err != nil {
			return "", fmt.Errorf("子代理推理失败：%w", err)
		}

		history = append(history, *resp)

		// 不再调用工具 = 它认为查完了，Content就是报告
		if len(resp.ToolCalls) == 0 {
			return strings.TrimSpace(resp.Content), nil
		}

		obs := make([]schema.Message, len(resp.ToolCalls))
		var wg sync.WaitGroup

		for i, call := range resp.ToolCalls {
			wg.Add(1)
			go func(idx int, c schema.ToolCall) {
				defer wg.Done()
				r := reg.Execute(ctx, c)
				log.Printf("[Subagent] %s -> %d 字节", c.Name, len(r.Output))
				obs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    r.Output,
					ToolCallID: c.ID,
				}
			}(i, call)
		}

		wg.Wait()

		history = append(history, obs...)
	}
}
