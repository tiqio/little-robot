package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"little-robot/internal/observability"
	"little-robot/internal/schema"
	"log"
	"sort"
)

// 让工具可以插拔，并且BaseTool是所有具体工具必须实现的最小契约
type BaseTool interface {
	Name() string
	Definition() schema.ToolDefinition
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// MiddlewareFunc在工具真正执行前拦截，allowed=false时工具不会执行
type MiddlewareFunc func(ctx context.Context, call schema.ToolCall) (allowed bool, rejectReason string)

type Registry interface {
	Register(tool BaseTool)
	Use(mw MiddlewareFunc)
	GetAvailableTools() []schema.ToolDefinition
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult // 这里有个细节，不会返回string, error是因为不希望把Agent弄死，ToolResult.IsError的设计意图：错误是给模型看的内容，而不是让程序崩溃的原因
}

type registryImpl struct {
	tools       map[string]BaseTool
	middlewares []MiddlewareFunc
}

func NewRegistry() Registry {
	return &registryImpl{
		tools:       make(map[string]BaseTool),
		middlewares: make([]MiddlewareFunc, 0),
	}
}

func (r *registryImpl) Use(mw MiddlewareFunc) {
	r.middlewares = append(r.middlewares, mw)
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
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)

	// defs := make([]schema.ToolDefinition, 0, len(r.tools))
	defs := make([]schema.ToolDefinition, 0, len(names))
	// for _, t := range r.tools {
	for _, n := range names { // 先给names排个序，然后用name那工具，否则每次prompt都不同（DeepSeek的上下文缓存命中率下降（前缀变了），会多付钱）
		// defs = append(defs, t.Definition())
		defs = append(defs, r.tools[n].Definition())
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

	// 在拿到tool之后，真正执行之前可以补充call信息，执行之后可以补充out信息
	ctx, span := observability.StartSpan(ctx, "tool:"+call.Name)
	defer span.End()
	span.Set("args_len", len(call.Arguments))
	span.Set("is_error", false) // 默认成功，失败路径覆盖它

	// 在路由完成后，真正执行前插一段中间件链，任何一个拒绝，工具就不执行
	// 为什么使用中间件而不是在每个工具里面加判断：审批、审计、限流、耗时统计都是“横切”的，中间件让你只写一次，而且工具自己不知道由审批这回事（跟BaseTool不知道模型存在是同一个设计哲学）
	for _, mw := range r.middlewares {
		allowed, reason := mw(ctx, call)
		if !allowed {
			log.Printf("[Registry] 工具%s被拦截：%s", call.Name, reason)
			span.Set("is_error", true)
			span.Set("blocked_by", "middleware")
			return schema.ToolResult{
				ToolCallID: call.ID,
				Output:     fmt.Sprintf("执行被系统拦截，原因：%s", reason),
				IsError:    true,
			}
		}
	}

	ctx, box := withImageCarrier(ctx)             // 工具通过它把产出的图片带回来
	out, err := tool.Execute(ctx, call.Arguments) // BaseTool只返回string和error，不反悔ToolResult，它不需要直到模型的存在
	if err != nil {
		span.Set("is_error", true)

		// 原始报错保持不动，命中已知特征时在后面追加一句"该怎么改"
		msg := fmt.Sprintf("执行%s失败：%v", call.Name, err)
		if hint := hintFor(call.Name, msg); hint != "" {
			span.Set("hint", "hit")
			msg += "\n\n【系统救援指南】" + hint
		} else {
			span.Set("hint", "miss")
		}

		return schema.ToolResult{ // Registry层负责把error翻译成ToolResult{IsError: true}，交给模型看
			ToolCallID: call.ID,
			Output:     msg,
			IsError:    true,
		}
	}

	//span.Set("is_error", false)
	span.Set("out_len", len(out))

	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     out,
		Images:     *box,
	}
}
