package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"little-robot/internal/mcp"
	"little-robot/internal/schema"
	"strings"
)

// MCPTool 把远端 MCP 工具包装成本地 BaseTool。
// 对引擎来说它和 bash/read_file 完全一样：同样的 Name/Definition/Execute。
type MCPTool struct {
	server string
	def    mcp.ToolDef
	client *mcp.Client
}

func NewMCPTool(server string, def mcp.ToolDef, client *mcp.Client) *MCPTool {
	return &MCPTool{server: server, def: def, client: client}
}

// Name 加前缀，避免和本地工具重名（远端 filesystem server 也有 read_file），
// 也方便在日志和审批规则里一眼看出是远端的。
func (t *MCPTool) Name() string {
	return "mcp__" + t.server + "__" + t.def.Name
}

func (t *MCPTool) Definition() schema.ToolDefinition {
	desc := t.def.Description
	if desc == "" {
		desc = t.def.Name
	}
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "[MCP:" + t.server + "] " + desc,
		// InputSchema 是 map[string]any，ToolDefinition.InputSchema 是 interface{}，直接赋
		InputSchema: t.def.InputSchema,
	}
}

// Execute 调远端并翻译结果。
// 约定：远端 isError 时返回 error —— 这样会走 registry 里统一转 ToolResult{IsError:true}
// 和救援指南那段逻辑，行为和本地工具一致。
func (t *MCPTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	res, err := t.client.CallTool(ctx, t.def.Name, args)
	if err != nil {
		return "", err
	}

	var text strings.Builder
	var images []string

	for _, block := range res.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
			text.WriteString("\n")
		case "image":
			if block.Data == "" {
				continue
			}
			mime := block.MimeType
			if mime == "" {
				mime = "image/png"
			}
			// 转成 data URL，塞进图片通道；引擎会把它们作为 user 消息注入给模型
			images = append(images, "data:"+mime+";base64,"+block.Data)
		case "resource":
			if block.Text != "" {
				text.WriteString(block.Text)
				text.WriteString("\n")
			}
		}
	}

	CarryImages(ctx, images...)

	out := strings.TrimSpace(text.String())
	if res.IsError {
		if out == "" {
			out = "MCP 工具返回错误"
		}
		return "", errors.New(out)
	}
	return out, nil
}

// 编译期断言：MCPTool 必须满足 BaseTool
var _ BaseTool = (*MCPTool)(nil)

// fmt 只是为了让示例里 fmt 有用；你可以删掉这行和 import
var _ = fmt.Sprintf
