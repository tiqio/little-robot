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
