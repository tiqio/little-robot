package tools

import (
	"context"
	"little-robot/internal/schema"
)

type Registry interface {
	GetAvailableTools() []schema.ToolDefination
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult // 这里有个细节，不会返回string, error是因为不希望把Agent弄死，ToolResult.IsError的设计意图：错误是给模型看的内容，而不是让程序崩溃的原因
}
