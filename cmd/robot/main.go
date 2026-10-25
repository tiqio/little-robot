package main

import (
	"context"
	"fmt"
	"little-robot/internal/guard"
	"little-robot/internal/observability"
	"little-robot/internal/session"
	"log"
	"os"
	"path/filepath"
	"time"

	"little-robot/internal/app"
	"little-robot/internal/config"
	"little-robot/internal/engine"
	"little-robot/internal/provider"
	"little-robot/internal/schema"
	"little-robot/internal/tools"
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

// Use implements [tools.Registry].
func (m *mockRegistry) Use(mw tools.MiddlewareFunc) {
	panic("unimplemented")
}

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
	// 命令行参数优先：-version / -h 必须在读 .env、校验 API Key 之前处理，
	// 否则没配环境变量时连版本号都打不出来。
	opts, handled, err := handleArgs(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if handled {
		return
	}

	// 使用自己的Provider
	if err := config.LoadEnvFile(".env"); err != nil {
		log.Printf("没有读到.env（%v），将只用系统环境变量", err)
	}

	llm := app.NewLLM()
	p := llm.Provider
	modelName := llm.Model

	tracker := observability.NewCostTracker(p, modelName, llm.Price)
	defer func() {
		log.Println(tracker.Report())
	}()

	/*
		workDir, err := os.Getwd()
		if err != nil {
			log.Fatalf("拿不到工作目录：%v", err)
		}
	*/

	workDir, err := filepath.Abs(opts.Dir) // 不能省，一定要绝对路径，否则后面resolvePath的沙箱判断会因为相对路径而失效
	if err != nil {
		log.Fatalf("解析工作区路径失败：%v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		log.Fatalf("创建工作区失败：%v", err)
	}

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Use(guard.Middleware(guard.NewTerminalApprover()))

	// 子代理只能使用只读工具（注意这里没有write_file/edit_file/spawn_subagent）
	subReg := tools.NewRegistry()
	subReg.Register(tools.NewReadFileTool(workDir))
	subReg.Register(tools.NewBashTool(workDir))
	subReg.Use(guard.Middleware(guard.NewTerminalApprover()))

	// eng := engine.NewAgentEngine(&mockProvider{}, &mockRegistry{}, workDir, true)
	//eng := engine.NewAgentEngine(p, registry, workDir, false)
	eng := engine.NewAgentEngine(tracker, registry, workDir, opts.Think)
	eng.SetContextTokens(llm.ContextTokens)
	eng.SetThinkingBudgets(llm.ThinkTokens, llm.ActTokens)
	eng.PlanMode = opts.Plan // 打开计划模式

	// 主注册表：在原有工具之外，多一个“派子代理”，但是这个时候需要eng来RunSub
	registry.Register(tools.NewSubagentTool(eng, subReg))

	// if err := eng.Run(context.Background(), "帮我检查当前目录的文件"); err != nil {
	// if err := eng.Run(context.Background(), "北京今天天气怎么样？适合出去跑步吗？"); err != nil {
	//if err := eng.Run(context.Background(), "读一下工作区里面的hello.txt，用一句话告诉我它写了什么？"); err != nil {
	//if err := eng.Run(context.Background(), "在工作区新建notes/demo.md，基于hello.txt文本中的内容写一首诗，用中文写"); err != nil {
	//if err := eng.Run(context.Background(), "查看一下当前目录下面有哪些文件"); err != nil {
	//if err := eng.Run(context.Background(), "帮我把notes/demo.md里的火改成风"); err != nil {
	//if err := eng.Run(context.Background(), "请并行执行三条bash命令，sleep 2 && echo A，sleep 2 && echo B，sleep 2 && echo C，然后打印三个echo时的时间戳"); err != nil {
	// if err := eng.Run(context.Background(), "读取一下工作区里面的hello.txt，用一句话告诉我它写了什么", engine.NewTerminalReporter()); err != nil {
	// 	log.Fatalf("引擎运行崩溃：%v", err)
	// }

	//ctx := context.Background()
	//
	//sessions := session.NewManager()
	//s := sessions.GetOrCreate("local", workDir) // id=local，这样后面eng可以挂到这个上面
	/*
		if err := eng.Run(ctx, s, "记住一件事：我叫阿伟，正在写little-robot这个Agent", nil); err != nil {
			log.Fatalf("第一轮失败：%v", err)
		}

		log.Println("===== 第一轮结束，开始第二轮 =====")

		if err := eng.Run(ctx, s, "我叫什么？我正在做什么？回答这两点", nil); err != nil {
			log.Fatalf("第二轮失败：%v", err)
		}
	*/

	/*
		三个真实的发现：
		1. 模型自己发明了文件记忆：文件写进了.claw/memory/user.md混起来了，.claw/memory被看成了它给自己做的笔记
		2. 记录时间：系统提示词里面没有当前日期，模型只能猜测
		3. 为了记住一个名字花费了5轮，因为不是能力问题
	*/

	// if err := eng.Run(ctx, s, "给little-robot加一个/version子命令：cmd/robot支持-version参数打印版本号，版本好号var Version=\"dev\"读，改完跑go build确认能过", engine.NewTerminalReporter()); err != nil {
	// if err := eng.Run(ctx, s, "把notes目录整个删掉", engine.NewTerminalReporter()); err != nil {
	//if err := eng.Run(ctx, s, "找出项目里所有调用NewAgentEngine的位置，说明每一处是干什么的", engine.NewTerminalReporter()); err != nil {
	//	log.Fatalf("引擎运行崩溃：%v", err) // %w是fmt.Errorf特有的，不能在fmt.Printf或者log.Printf或者log.Fatalf中使用
	//}

	// tool:spawn_subagent 4621 -> 54：非常贵，为子代理付的钱，被自己的压缩器丢掉了

	// 为什么给整个Run加30分钟超时？需要允许长程任务超时
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// 会话持久化：同名-session会接着上次的历史继续
	sessions := session.NewPersistentManager(filepath.Join(workDir, ".claw", "sessions"))
	s := sessions.GetOrCreate(opts.SessionID, workDir) // workDir被用于恢复时覆盖session文件状态
	if s.Len() > 0 {                                   // 看s.history有没有曾经的会话记录
		log.Printf("[CLI] 恢复会话%q，已有历史%d条", s.ID, s.Len()) // 为什么Run出错用log.Printf？因为后面要保存会话，直接Fatal会让这次跑的全部上下午丢掉，下次没法续，先落盘，再退出
	} else {
		log.Printf("[CLI] 新会话%q", s.ID)
	}

	if err := eng.Run(ctx, s, opts.Prompt, engine.NewTerminalReporter()); err != nil {
		log.Printf("引擎运行失败：%v", err)
	}

	// sessions的存储在引擎运行成功之后，可以保存会话，不在Session.Append里自动写是因为：
	// 1. 没追加一条消息就写一次磁盘（IO放大） 2. 写失败没法处理 3. 并发写要额外加锁
	// 所以：显式保存，时机由调用方决定——这是正确的分层
	if path, err := sessions.Save(s); err != nil {
		log.Printf("[CLI] 保存会话失败：%v", err)
	} else if path != "" {
		log.Printf("[CLI] 会话已保存：%s（历史%d条）", path, s.Len())
	}
}
