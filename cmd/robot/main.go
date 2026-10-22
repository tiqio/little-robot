package main

import (
	"context"
	"little-robot/internal/engine"
	"little-robot/internal/provider"
	"little-robot/internal/schema"
	"little-robot/internal/tools"
	"log"
	"os"
)

var (
	_ provider.LLMProvider = &mockProvider{}
	_ tools.Registry       = &mockRegistry{}
)

type mockProvider struct {
	turn int
}

func (m *mockProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefination) (*schema.Message, error) {
	m.turn++
	if m.turn == 1 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "让我看看当前目录下有什么文件",
			ToolCalls: []schema.ToolCall{
				{
					ID:        "call_123",
					Name:      "bash",
					Arguments: []byte(`"command": "ls -la"`),
				},
			},
		}, nil
	}

	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "看到了文件列表，里面有main.go，任务完成",
	}, nil
}

type mockRegistry struct{}

func (m *mockRegistry) GetAvailableTools() []schema.ToolDefination {
	return nil
}

func (m *mockRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     "-rw-r--r-- 1 user group 234 Oct 24 10:00 main.go\n",
		IsError:    false,
	}
}

func main() {
	workDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("拿不到工作目录：%v", err)
	}

	// TODO 1（大脑）：第 4 章接入真实的模型 Provider
	// TODO 2（手脚）：第 5-8 章注册 read_file / write_file / bash / edit_file
	// TODO 3（记忆）：第 10-13 章接入上下文管理与会话持久化
	// TODO 4（心脏）：第 2 章用 engine.NewAgentEngine(...) 把上面三个接起来

	eng := engine.NewAgentEngine(&mockProvider{}, &mockRegistry{}, workDir)

	if err := eng.Run(context.Background(), "帮我检查当前目录的文件"); err != nil {
		log.Fatalf("引擎运行崩溃：%v", err)
	}
}
