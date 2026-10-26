// Package app 负责"用本地还是云端、连哪个地址、按什么价格计费"这类装配决定，
// 让三个 cmd/* 只需一行拿到可用的 provider。
package app

import (
	"context"
	"log"
	"time"

	"little-robot/internal/config"
	"little-robot/internal/observability"
	"little-robot/internal/provider"
)

// LLM 是装配结果：provider + 计价用的模型名 + 单价 + 上下文窗口
type LLM struct {
	Provider provider.LLMProvider
	Model    string
	Price    observability.Price // 本地模型为零值 = 不计费
	Local    bool

	// ContextTokens 决定"历史什么时候开始压缩"。本地从 LM Studio 探测，云端写死 128K。
	ContextTokens int

	// Thinking 思考/行动分家（ch33/S2）：思考型模型跑两段——
	// 思考段（收走工具、ThinkTokens 预算）→ 行动段（挂回工具、ActTokens 预算）。
	// 两段预算分开，思考再长也吃不到行动的份，"思考吃光额度 → 静默退出"的悬崖就没了。
	// 代价：每个 Run 多一次 LLM 调用，所以做成开关（THINKING），本地默认开、云端默认关。
	Thinking    bool
	ThinkTokens int
	ActTokens   int

	// CompactionRatio 历史占到窗口的多少就开始压缩（默认 0.40）。
	// 调大 = 少压缩、记得多、但预填充更慢；调小 = 压得勤、模型更健忘。
	CompactionRatio float64
	// ProtectChars 保护区体积预算（字符）：最近这段记忆保持原样，更早的折叠成占位。
	ProtectChars int
}

// NewLLM 按 USE_LOCAL 选后端：
//
//	USE_LOCAL=true  → LOCAL_BASE_URL + LOCAL_MODEL（LM Studio，无需 key）
//	USE_LOCAL=false → REMOTE_BASE_URL + REMOTE_MODEL（默认 DeepSeek）
//
// 地址和模型都从环境变量来，换机器/换模型不用改代码。
func NewLLM() LLM {
	if useLocal() {
		baseURL := config.Get("LOCAL_BASE_URL", "http://127.0.0.1:1234/v1")
		model := config.Get("LOCAL_MODEL", "huihui-qwen3.8-27b-abliterated@iq2_s")

		// 上下文窗口不让人配：直接问 LM Studio 实际加载了多大
		contextTokens := provider.LMStudioContext(context.Background(), baseURL, model)
		if contextTokens == 0 {
			contextTokens = 32 * 1024
			log.Printf("[LLM] 本地 %s（查不到上下文长度，按 %d tokens 保守处理）", model, contextTokens)
		} else {
			log.Printf("[LLM] 本地 %s @ %s，已加载上下文 %d tokens", model, baseURL, contextTokens)
		}

		return LLM{
			Provider:      provider.NewLMStudioProvider(baseURL, model),
			Model:         model,
			Local:         true,
			ContextTokens: contextTokens,
			// 本地 27B 是思考型模型：默认开两段。思考 2048 / 行动 1024，
			// 14 tok/s 下典型一轮 ≈ 思考 20-40s + 行动 5-15s，飞书 10 分钟超时兜得住。
			Thinking:    config.Bool("THINKING", false),
			ThinkTokens: config.Int("LOCAL_THINK_TOKENS", 2048),
			ActTokens:   config.Int("LOCAL_ACT_TOKENS", 1024),
		}
	}

	baseURL := config.Get("REMOTE_BASE_URL", "https://api.deepseek.com")
	model := config.Get("REMOTE_MODEL", "deepseek-flash")
	apiKey := config.Get("REMOTE_API_KEY", config.Get("DEEPSEEK_API_KEY", ""))
	log.Printf("[LLM] 云端 %s @ %s", model, baseURL)

	return LLM{
		Provider: provider.NewDeepSeekProvider(apiKey, baseURL, model),
		Model:    model,
		Price:    observability.PriceForNow(time.Now()),
		Local:    false,
		// 云端接口不返回窗口大小，DeepSeek 系列是 128K
		ContextTokens: 128 * 1024,
		// 云端默认单段（DeepSeek 非思考版）；用思考版（reasoner）时 THINKING=on
		// 预算 0 = 不覆盖 provider 默认
		Thinking: config.Bool("THINKING", false),
	}
}

// useLocal 只认 "true"/"1"/"yes"，其余一律当云端——宁可花钱也别连不上。
func useLocal() bool {
	switch config.Get("USE_LOCAL", "false") {
	case "true", "TRUE", "True", "1", "yes", "on":
		return true
	}
	return false
}
