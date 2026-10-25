package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"little-robot/internal/schema"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var _ LLMProvider = &OpenAIProvider{}

// ===== 线格式（DeepSeek/OpenAI兼容协议）=====

type chatMessage struct {
	Role string `json:"role"`
	// Content 既可能是 string，也可能是 []contentBlock（带图时）
	Content    any            `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// contentBlock 对应 OpenAI 兼容的多段 content
type contentBlock struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// chatResponseMessage 是响应里的 assistant 消息：content 一定是字符串，
// 不能复用请求侧的 chatMessage（那边 Content 可能是数组）
type chatResponseMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []chatToolCall `json:"tool_calls"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // 注意这里是JSON字符串，不是对象？
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatToolFunc `json:"function"`
}

type chatToolFunc struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type chatRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens,omitempty"`
	Messages  []chatMessage `json:"messages"`
	// Tools    []chatTool    `json:"tols,omitempty"` 发现这里没有调用Tool是因为没有传过去，为什么这里一点错误都不报？因为tag是字符串，编译器不检查内容，它不知道自己有get_weather，只能说“我查不出实时天气”，这是最危险的一类bug：静默失效，看prompt token能发现这里涨了很多
	Tools []chatTool `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatResponseMessage `json:"message"`
		FinishReason string              `json:"finish_reason"` // stop / length / tool_calls / content_filter
	} `json:"choices"`
	Usage struct {
		PromptTokens          int `json:"prompt_tokens"`
		CompletionTokens      int `json:"completion_tokens"`
		TotalTokens           int `json:"total_tokens"`
		PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
		PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"` // 将这个Error弄成一个指针，当Error为nil的时候GO在生成JSON的时候会完全去除error建，如果加上了omitempty，当Token为0的时候，Go会在序列化JSON的时候直接删除这个字段，如果存在并有内容，Go解码后的resp.Error会指向新分配结构体的指针
}

// Config 描述一个 OpenAI 兼容端点。
// DeepSeek、LM Studio、vLLM、Ollama(/v1) 都走同一套协议，差别只在
// baseURL / 鉴权 / 超时 —— 这些差异全收在这里，Generate 一份就够。
type Config struct {
	Name      string        // 日志与错误里显示的名字
	BaseURL   string        // 例如 https://api.deepseek.com 或 http://127.0.0.1:1234/v1
	APIKey    string        // 空 = 不发 Authorization（本地服务通常不校验）
	Model     string        // 模型 id
	Timeout   time.Duration // 单次请求上限
	MaxTokens int           // 单次回复上限；本地小模型容易"想太多"，必须设
}

type OpenAIProvider struct {
	cfg    Config
	client *http.Client
}

func NewOpenAI(cfg Config) *OpenAIProvider {
	if cfg.Name == "" {
		cfg.Name = "openai"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	return &OpenAIProvider{
		cfg: Config{
			Name:      cfg.Name,
			BaseURL:   strings.TrimRight(cfg.BaseURL, "/"),
			APIKey:    cfg.APIKey,
			Model:     cfg.Model,
			MaxTokens: cfg.MaxTokens,
		},
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// NewDeepSeekProvider 保留原签名，云端走这条
func NewDeepSeekProvider(apiKey, baseURL, model string) *OpenAIProvider {
	return NewOpenAI(Config{
		Name: "deepseek", BaseURL: baseURL, APIKey: apiKey, Model: model,
		Timeout: 120 * time.Second,
	})
}

// NewLMStudioProvider 本地 LM Studio（或 llama.cpp server，同一套 OpenAI 协议）：
// 默认不需要 key，但 27B 首次加载可能很慢，超时给到 10 分钟；真正的上限由 ctx 控制。
func NewLMStudioProvider(baseURL, model string) *OpenAIProvider {
	// 思考型模型（reasoning_content 单独算 token）一次能想 2000+ token：
	// max_tokens 给小了，思考吃光额度就"说了个寂寞"（finish_reason=length + 空 content + 静默退出）。
	// 默认 2048，LOCAL_MAX_TOKENS 可覆盖（14 tok/s 下 2048 ≈ 最差 2.4 分钟）。
	maxTokens := 2048
	if v, err := strconv.Atoi(os.Getenv("LOCAL_MAX_TOKENS")); err == nil && v > 0 {
		maxTokens = v
	}
	return NewOpenAI(Config{
		Name: "lmstudio", BaseURL: baseURL, APIKey: "", Model: model,
		Timeout:   10 * time.Minute,
		MaxTokens: maxTokens,
	})
}

func (p *OpenAIProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	reqBody := chatRequest{
		Model:     p.cfg.Model,
		Messages:  toChatMessages(msgs),
		Tools:     toChatTools(availableTools),
		MaxTokens: p.cfg.MaxTokens,
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败：%w", err)
	}

	if os.Getenv("LLM_REQUEST_DEBUG") == "true" {
		// log.Printf("[LLMProvider] 请求体：%s", string(raw))
		log.Printf("[LLMProvider] 请求体：%s", prettyJSON(raw))
	}

	// 注意NewRequestWithContext: ctx在这里第一次生效，ctx支持超时限制，主动取消以及链路追踪
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// 本地服务通常不校验 key，空值就别发空 Bearer（有些实现会因此 401）
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求%s失败：%w", p.cfg.Name, err)
	}
	defer resp.Body.Close()

	respRaw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DeepSeek 返回 %d: %s", resp.StatusCode, truncate(string(respRaw), 500))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respRaw, &parsed); err != nil {
		return nil, fmt.Errorf("解析响应失败：%w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("DeepSeek 报错：%s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("响应里面没有choices")
	}

	// 观测的活交给tracker，否则每次调用打两遍
	//log.Printf("[Provider] %s | tokens: prompt=%d completion=%d total=%d", p.model, parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, parsed.Usage.TotalTokens)

	m := parsed.Choices[0].Message
	out := &schema.Message{
		Role:         schema.RoleAssistant, // 返回的数据用RoleAssistant受理
		Content:      m.Content,
		FinishReason: parsed.Choices[0].FinishReason,
	}
	out.Usage = &schema.Usage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
		CacheHitTokens:   parsed.Usage.PromptCacheHitTokens,
		CacheMissTokens:  parsed.Usage.PromptCacheMissTokens,
	}

	for _, tc := range m.ToolCalls {
		if tc.Type != "function" {
			continue
		}
		args := tc.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, schema.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			// Arguments: json.RawMessage{args}, 编译器以为你要用args当元素造一个[]byte，但是用()可以进行类型转换，Go类型转换允许string到[]byte，校验要到工具里json.Unmarshal才发生
			Arguments: json.RawMessage(args),
		})
	}
	return out, nil
}

// 翻译我们的格式 ——> 厂商格式
func toChatMessages(msgs []schema.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msgs))
	for _, m := range msgs {
		cm := chatMessage{
			Role: string(m.Role),
		}
		// 带图且是 user 消息 → content 用数组；否则保持字符串（前缀缓存更稳）
		if len(m.Images) > 0 && m.Role == schema.RoleUser {
			blocks := make([]contentBlock, 0, len(m.Images)+1)
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, contentBlock{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				blocks = append(blocks, contentBlock{
					Type:     "image_url",
					ImageURL: &imageURL{URL: img, Detail: "auto"},
				})
			}
			cm.Content = blocks
		} else {
			cm.Content = m.Content
		}
		switch m.Role {
		case schema.RoleUser:
			// 如果是User并且ToolCallID不是0，就需要翻译成role=tool，之前偷懒弄成User了（工具结果被伪装成User）
			if m.ToolCallID != "" {
				cm.Role = "tool"
				cm.ToolCallID = m.ToolCallID
			}
		case schema.RoleAssistant:
			for _, tc := range m.ToolCalls {
				cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: chatFunctionCall{
						Name:      tc.Name,
						Arguments: string(tc.Arguments), // RawMessage ——> JSON字符串
					},
				})
			}
		}
		out = append(out, cm)
	}
	return out
}

func toChatTools(defs []schema.ToolDefinition) []chatTool {
	if len(defs) == 0 {
		return nil
	}
	out := make([]chatTool, 0, len(defs))
	for _, d := range defs {
		out = append(out, chatTool{
			Type: "function",
			Function: chatToolFunc{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.InputSchema,
			},
		})
	}
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func prettyJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "\t"); err != nil {
		return string(raw) // 缩进失败就原样返回，别让日志把程序搞崩
	}
	return buf.String()
}
