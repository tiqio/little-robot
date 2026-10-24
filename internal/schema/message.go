package schema

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Usage 记录一次调用消耗的token，由provider填充；mock或老provider可以留nil
type Usage struct {
	// 修正原笔误：PromptToken -> PromptTokens，json tag 补复数
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// DeepSeek 缓存拆分明细：PromptTokens == CacheHitTokens + CacheMissTokens
	// 其他厂商不返回时保持 0
	CacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	CacheMissTokens int `json:"prompt_cache_miss_tokens"`
}

// 所有人用，一条对话消息（系统的/用户的/模型的/工具结果的）
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"` // ToolCalls为空的时候不出现在JSON里面，ToolCallID为空的时候也不出现，发给模型的请求里面多一个"tool_calls": null，有些后端直接400
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`

	// Images 只用于 user 消息：data URL 形式（data:image/jpeg;base64,...）
	// 厂商只接受 user 角色带图，system/assistant 带图会 400
	Images []string `json:"images,omitempty"`
}

// 你给模型，”我有哪些工具，每个要什么参数“，user
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

// 模型给你，”我要调用bash，参数是这个“，assistant
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // 如果用的是string，"arguments": "{\"city\":\"北京\"}"，里面的字符串会被转义导致Unmarshal失败，而且原样存着可以让工具去报错，而不是循环的时候报错
}

// 你给模型，”bash跑完了，输出是这个，成没成功“，？？？
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Output     string `json:"output"`
	IsError    bool   `json:"is_error"`

	// Images：工具产生的图片（data URL），engine 会把它作为一条额外的 user 消息注入，
	// 让模型"看见"结果。厂商只允许 user 消息带图，所以不能塞在 tool 消息里。
	Images []string `json:"images,omitempty"`
}
