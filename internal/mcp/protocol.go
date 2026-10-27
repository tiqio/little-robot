package mcp

import "encoding/json"

// 请求/响应统一信封，Result和Error二选一
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"` // 通知没有id
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ToolDef 是服务端声明的工具，InputSchema直接就是JSON Schema，可以原样塞进schema.ToolDefinition.InputSchema(interface{})，不需要转换
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"` // 这里是interface，map[string]any就可以直接塞进去，不需要tsbot那样用z.fromJSONSchema转换（GO比TS省事）
}

// ContentBlock 是工具返回的内容块：文本/图片/内嵌资源
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// CallResult 是tool/call的result
type CallResult struct {
	Content []ContentBlock `json:"content,omitempty"`
	IsError bool           `json:"isError,omitempty"`
}

const ProtocolVersion = "2025-06-18"
