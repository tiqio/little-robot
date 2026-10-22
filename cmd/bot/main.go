package main

import (
	"context"
	"little-robot/internal/config"
	"little-robot/internal/engine"
	"little-robot/internal/feishu"
	"little-robot/internal/provider"
	"little-robot/internal/tools"
	"log"
	"os"
	"path/filepath"
)

func main() {
	_ = config.LoadEnvFile(".env")

	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")
	if apiKey == "" || appID == "" || appSecret == "" {
		log.Fatal("需要 DEEPSEEK_API_KEY / FEISHU_API_ID / FEISHU_APP_SECRET")
	}

	// 飞书带来的任务不能放在代码仓库里，单独开工作区
	workDir := config.Get("ROBOT_WORKDIR", "./workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		log.Fatalf("创建工作区失败：%v", err)
	}
	workDir, _ = filepath.Abs(workDir)
	log.Printf("[Bot] 工作区：%s", workDir)

	p := provider.NewDeepSeekProvider(
		apiKey,
		config.Get("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		config.Get("DEEPSEEK_MODEL", "deepseek-flash"),
	)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))

	eng := engine.NewAgentEngine(p, registry, workDir, false)

	bot := feishu.NewBot(appID, appSecret, eng)
	if err := bot.Start(context.Background()); err != nil {
		log.Fatalf("飞书长连接启动失败：%v", err)
	}
}
