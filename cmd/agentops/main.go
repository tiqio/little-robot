package main

import (
	"context"
	"little-robot/internal/app"
	"little-robot/internal/config"
	"little-robot/internal/engine"
	"little-robot/internal/feishu"
	"little-robot/internal/guard"
	"little-robot/internal/observability"
	"little-robot/internal/session"
	"little-robot/internal/tools"
	"log"
	"os"
	"path/filepath"
)

func main() {
	_ = config.LoadEnvFile(".env")

	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		log.Fatal("需要 FEISHU_APP_ID / FEISHU_APP_SECRET")
	}

	// 1. 工作区：飞书来的任务绝对不能落在代码仓库里面
	workDir := config.Get("ROBOT_WORKDIR", "./workspace")
	log.Printf("[AgentOps] 工作区：%s", workDir)

	// 2. 大脑+成本观测（装饰器）
	llm := app.NewLLM()
	p := llm.Provider
	tracker := observability.NewCostTracker(p, llm.Model, llm.Price)

	// 3. 手脚：主注册表挂只读+写+Shell，统一过DenyApprover
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewSendImageTool(workDir))
	registry.Register(tools.NewImageTool(workDir, os.Getenv("PEXELS_API_KEY")))
	registry.Register(tools.NewViewImageTool(workDir))
	registry.Use(guard.Middleware(&guard.DenyApprover{}))

	// 4. 子代理只用只读工具
	subReg := tools.NewRegistry()
	subReg.Register(tools.NewReadFileTool(workDir))
	subReg.Register(tools.NewBashTool(workDir))
	subReg.Use(guard.Middleware(&guard.DenyApprover{}))

	// 5. 心脏
	eng := engine.NewAgentEngine(tracker, registry, workDir, false)
	eng.SetContextTokens(llm.ContextTokens)
	//eng.PlanMode = true
	registry.Register(tools.NewSubagentTool(eng, subReg))

	// 6. 会话：落在workspace/.claw/sessions，重启也不丢失
	// 会话按chat_id分文件：workspace/.claw/sessions/oc_xxx.json —— 群A的记忆和群B完全隔离，这是飞书场景最自然的隔离力度
	mgr := session.NewPersistentManager(filepath.Join(workDir, ".claw", "sessions"))

	// 7. 感官：飞书长连接
	bot := feishu.NewBot(appID, appSecret, eng)
	bot.UseSessions(mgr)

	log.Printf("[AgentOps] 长连接启动中...")
	if err := bot.Start(context.Background()); err != nil {
		log.Fatalf("飞书长连接启动失败：%v", err)
	}

	// 8. 退出时打一张成本总账
	log.Println(tracker.Report())
}
