package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"little-robot/internal/schema"
	"log"
	"strings"
)

/*
	子代理自己去读20个文件找答案，结果是：20个文件的内容永久留在会话里，之后每一轮都要重发，上下文被撑爆，钱也烧掉
		1. 上下文隔离：子代理的阅读过程不会污染主会话
		2. 工具裁剪：子代理只拿只读工具，物理上改不了东西
		3. 结构性防递归：子代理的工具列表中没有spawn_subagent，开不了孙代理
	接口定义在消费方（tools），实现由提供方（engine）提供，因为：engine已经import tools，如果tools再import engine就循环依赖了
*/

// AgentRunner由engine实现，定义在这里是为了避免tools反向依赖engine
type AgentRunner interface {
	RunSub(ctx context.Context, taskPrompt string, reg Registry) (string, error)
}

var (
	_ BaseTool = &SubagentTool{}
)

type SubagentTool struct {
	runner AgentRunner
	reg    Registry // 子代理能用的工具集（只读）
}

func NewSubagentTool(r AgentRunner, reg Registry) *SubagentTool {
	return &SubagentTool{
		runner: r,
		reg:    reg,
	}
}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

func (t *SubagentTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "派一个只读的探索子代理去找东西，当你需要阅读大量文件、跨文件追踪关系时用它：它探索玩只回一份精炼摘要，不会把读到的内容塞进你的上下文",
		// Description: "派一个只读的探索子代理去找东西，当你需要阅读大量文件、跨文件追踪关系时用它：它探索玩只回一份精炼摘要，不会把读到的内容塞进你的上下文，" +
		// 	"优先调用本工具，让它把结论带回来；不要自己逐个read_file把大量源码塞入上下文"
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_prompt": map[string]interface{}{
					"type":        "string",
					"description": "给子代理的明确命令，例如：找出所有调用NewAgentEngine的位置，说明各自用处",
				},
			},
			"required": []string{"task_prompt"},
		},
	}
}

func (t *SubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		TaskPrompt string `json:"task_prompt"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%w", err)
	}
	if strings.TrimSpace(in.TaskPrompt) == "" {
		return "", fmt.Errorf("task_prompt不能为空")
	}

	log.Printf("[Subagent] 委派：%s", in.TaskPrompt)

	summary, err := t.runner.RunSub(ctx, in.TaskPrompt, t.reg)
	if err != nil {
		return "", fmt.Errorf("子代理执行失败：%w", err)
	}

	return "[子代理报告]\n" + summary, nil
}
