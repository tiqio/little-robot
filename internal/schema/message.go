package schema

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// 所有人用，一条对话消息（系统的/用户的/模型的/工具结果的）
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"` // ToolCalls为空的时候不出现在JSON里面，ToolCallID为空的时候也不出现，发给模型的请求里面多一个"tool_calls": null，有些后端直接400
	ToolCallID string     `json:"tool_call_id,omitempty"`
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
}
