package main

import (
	"context"
	"little-robot/internal/config"
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

func (m *mockProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	// main里面mock里需要区分有没有工具
	if len(tools) == 0 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "[推理中]目标是检查用户，不能盲猜，我需要先调用bash执行ls，看清楚有什么再判断",
		}, nil // 这行不消耗turn计数，所以两阶段的轮次不会错位
	}

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

// Register implements [tools.Registry].
func (m *mockRegistry) Register(tool tools.BaseTool) {
	panic("unimplemented")
}

/*
func (m *mockRegistry) GetAvailableTools() []schema.ToolDefinition {
	return []schema.ToolDefinition{
		{
			Name: "bash",
		},
	}
}

func (m *mockRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     "-rw-r--r-- 1 user group 234 Oct 24 10:00 main.go\n",
		IsError:    false,
	}
}
*/

func (m *mockRegistry) GetAvailableTools() []schema.ToolDefinition {
	return []schema.ToolDefinition{
		{
			Name:        "get_weather",
			Description: "查询指定城市的当前天气",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{
						"type":        "string",
						"description": "城市名称，例如北京",
					},
				},
				"required": []string{"city"},
			},
		}, // 注意如果是数组或者对象的话记得加","
	}
}

func (m *mockRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	if call.Name != "get_weather" {
		return schema.ToolResult{
			ToolCallID: call.ID,
			Output:     "未知工具：" + call.Name,
			IsError:    true,
		}
	}
	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     `{"city":"北京","weather":"晴天","temp_c":25,"wind":"微风"}`,
		IsError:    false,
	}
}

func main() {
	// 使用自己的Provider
	if err := config.LoadEnvFile(".env"); err != nil {
		log.Printf("没有读到.env（%v），将只用系统环境变量", err)
	}

	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		log.Fatal("DEEPSEEK_API_KEY没有设置，检查.env")
	}

	p := provider.NewDeepSeekProvider(
		apiKey,
		config.Get("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		config.Get("DEEPSEEK_MODEL", "deepseek-flash"),
	)

	workDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("拿不到工作目录：%v", err)
	}

	// TODO 1（大脑）：第 4 章接入真实的模型 Provider
	// TODO 2（手脚）：第 5-8 章注册 read_file / write_file / bash / edit_file
	// TODO 3（记忆）：第 10-13 章接入上下文管理与会话持久化
	// TODO 4（心脏）：第 2 章用 engine.NewAgentEngine(...) 把上面三个接起来

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))

	// eng := engine.NewAgentEngine(&mockProvider{}, &mockRegistry{}, workDir, true)
	eng := engine.NewAgentEngine(p, registry, workDir, false)

	// if err := eng.Run(context.Background(), "帮我检查当前目录的文件"); err != nil {
	// if err := eng.Run(context.Background(), "北京今天天气怎么样？适合出去跑步吗？"); err != nil {
	//if err := eng.Run(context.Background(), "读一下工作区里面的hello.txt，用一句话告诉我它写了什么？"); err != nil {
	//if err := eng.Run(context.Background(), "在工作区新建notes/demo.md，基于hello.txt文本中的内容写一首诗，用中文写"); err != nil {
	if err := eng.Run(context.Background(), "查看一下当前目录下面有哪些文件"); err != nil {
		log.Fatalf("引擎运行崩溃：%v", err)
	}
}
