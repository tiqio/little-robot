package main

import (
	"context"
	"little-robot/internal/app"
	"little-robot/internal/config"
	"little-robot/internal/engine"
	"little-robot/internal/feishu"
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

	// 飞书带来的任务不能放在代码仓库里，单独开工作区
	workDir := config.Get("ROBOT_WORKDIR", "./workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		log.Fatalf("创建工作区失败：%v", err)
	}
	workDir, _ = filepath.Abs(workDir)
	log.Printf("[Bot] 工作区：%s", workDir)

	// 模型可切：LLM_PROVIDER=lmstudio 就用本地 LM Studio，否则走 DeepSeek
	llm := app.NewLLM()
	p := llm.Provider

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewSendImageTool(workDir))
	registry.Register(tools.NewImageTool(workDir, os.Getenv("PEXELS_API_KEY")))
	registry.Register(tools.NewViewImageTool(workDir))

	eng := engine.NewAgentEngine(p, registry, workDir, false)
	eng.SetContextTokens(llm.ContextTokens)

	bot := feishu.NewBot(appID, appSecret, eng)
	if err := bot.Start(context.Background()); err != nil {
		log.Fatalf("飞书长连接启动失败：%v", err)
	}
}
