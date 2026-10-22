package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"little-robot/internal/schema"
	"log"
)

// 让工具可以插拔，并且BaseTool是所有具体工具必须实现的最小契约
type BaseTool interface {
	Name() string
	Definition() schema.ToolDefinition
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

type Registry interface {
	Register(tool BaseTool)
	GetAvailableTools() []schema.ToolDefinition
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult // 这里有个细节，不会返回string, error是因为不希望把Agent弄死，ToolResult.IsError的设计意图：错误是给模型看的内容，而不是让程序崩溃的原因
}

type registryImpl struct {
	tools map[string]BaseTool
}

func NewRegistry() Registry {
	return &registryImpl{
		tools: make(map[string]BaseTool),
	}
}

func (r *registryImpl) Register(tool BaseTool) {
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		log.Printf("[Registry]警告：工具%q已经存在，将被覆盖\n", name)
	}
	r.tools[name] = tool
	log.Printf("[Registry] 挂载工具：%s\n", name)
}

func (r *registryImpl) GetAvailableTools() []schema.ToolDefinition {
	defs := make([]schema.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	return defs
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	tool, ok := r.tools[call.Name]
	if !ok {
		return schema.ToolResult{ // 模型偶尔会幻觉出一个不存在的工具名称，这个时候要给模型一句“没有这个工具”，它才会改用别的
			ToolCallID: call.ID,
			Output:     fmt.Sprintf("系统中不存在名为%q的工具", call.Name),
			IsError:    true,
		}
	}

	out, err := tool.Execute(ctx, call.Arguments) // BaseTool只返回string和error，不反悔ToolResult，它不需要直到模型的存在
	if err != nil {
		return schema.ToolResult{ // Registry层负责把error翻译成ToolResult{IsError: true}，交给模型看
			ToolCallID: call.ID,
			Output:     fmt.Sprintf("执行%s失败：%v", call.Name, err),
			IsError:    true,
		}
	}

	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     out,
	}
}
