package observability

import (
	"context"
	"fmt"
	"little-robot/internal/provider"
	"little-robot/internal/schema"
	"log"
	"sync"
	"time"
)

// Price 价格单位：元 / 百万 tokens（deepseek-flash）
// 来源：https://api-docs.deepseek.com/zh-cn/quick_start/pricing/
// 缓存命中和未命中的输入不同价，所以要分开算
type Price struct {
	InputHitPerM  float64 // 输入（缓存命中）
	InputMissPerM float64 // 输入（缓存未命中）
	OutputPerM    float64 // 输出
}

// 高峰时段：北京时间 周一至周五 9:00-12:00、14:00-18:00（不含法定节假日）
// 空闲时段价格是高峰的一半
var (
	PeakPrice    = Price{InputHitPerM: 0.04, InputMissPerM: 2.0, OutputPerM: 8.0}
	OffPeakPrice = Price{InputHitPerM: 0.02, InputMissPerM: 1.0, OutputPerM: 4.0}
)

// PriceForNow 按北京时间判断峰谷。
// 没有处理法定节假日，所以节假日会被当成高峰，属于“宁可高估”的保守口径。
func PriceForNow(t time.Time) Price {
	bj := t.In(time.FixedZone("CST", 8*3600))
	if wd := bj.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return OffPeakPrice
	}
	if h := bj.Hour(); (h >= 9 && h < 12) || (h >= 14 && h < 18) {
		return PeakPrice
	}
	return OffPeakPrice
}

// CostTracker 是包在 provider 外面的装饰器：计时、记账，然后转发
type CostTracker struct {
	next  provider.LLMProvider
	model string
	price Price

	mu           sync.Mutex
	calls        int
	failedCalls  int
	hitTokens    int
	missTokens   int
	outputTokens int
	totalLatency time.Duration
}

var _ provider.LLMProvider = &CostTracker{}

func NewCostTracker(next provider.LLMProvider, model string, price Price) *CostTracker {
	return &CostTracker{next: next, model: model, price: price}
}

func (t *CostTracker) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	start := time.Now()
	resp, err := t.next.Generate(ctx, msgs, tools)
	latency := time.Since(start)

	if err != nil {
		t.mu.Lock()
		t.failedCalls++
		t.mu.Unlock()
		log.Printf("[Cost] %s 调用失败（耗时%v）：%v", t.model, latency.Round(time.Millisecond), err)
		return nil, err
	}

	var hit, miss, out int
	if resp.Usage != nil {
		hit = resp.Usage.CacheHitTokens
		miss = resp.Usage.CacheMissTokens
		out = resp.Usage.CompletionTokens
		// 后端没返回缓存拆分字段时，把整个输入都算成未命中（保守估计）
		if hit == 0 && miss == 0 {
			miss = resp.Usage.PromptTokens
		}
	}

	t.mu.Lock()
	t.calls++
	t.hitTokens += hit
	t.missTokens += miss
	t.outputTokens += out
	t.totalLatency += latency
	t.mu.Unlock()

	log.Printf("[Cost] %s | 命中%d 未命中%d 输出%d | 命中率%.0f%% | %v | %.6f元",
		t.model, hit, miss, out, hitRatio(hit, miss),
		latency.Round(time.Millisecond), t.price.Cost(hit, miss, out))

	return resp, nil
}

// Cost 按命中/未命中/输出三段分别计价
func (p Price) Cost(hit, miss, out int) float64 {
	return float64(hit)/1e6*p.InputHitPerM +
		float64(miss)/1e6*p.InputMissPerM +
		float64(out)/1e6*p.OutputPerM
}

func hitRatio(hit, miss int) float64 {
	if hit+miss == 0 {
		return 0
	}
	return float64(hit) * 100 / float64(hit+miss)
}

// Report 返回汇总，进程结束时打印
func (t *CostTracker) Report() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.calls == 0 {
		return "[Cost] 没有成功调用"
	}

	avg := t.totalLatency / time.Duration(t.calls)
	total := t.price.Cost(t.hitTokens, t.missTokens, t.outputTokens)

	return fmt.Sprintf(
		"[Cost] %s 汇总：%d次（失败%d）｜输入%d（命中%d/未命中%d，命中率%.0f%%）｜输出%d｜均延迟%v｜总计%.6f元",
		t.model, t.calls, t.failedCalls,
		t.hitTokens+t.missTokens, t.hitTokens, t.missTokens,
		hitRatio(t.hitTokens, t.missTokens),
		t.outputTokens, avg.Round(time.Millisecond), total)
}
