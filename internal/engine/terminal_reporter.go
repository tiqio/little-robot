package engine

import (
	"context"
	"fmt"
	"little-robot/internal/schema"
)

var (
	_ Reporter = &TerminalReporter{}
)

// TerminalReporter 把事件打到终端，是默认实现
type TerminalReporter struct{}

func NewTerminalReporter() *TerminalReporter {
	return &TerminalReporter{}
}

func (r *TerminalReporter) OnThinkingStart(ctx context.Context) {}

func (r *TerminalReporter) OnThinkingText(ctx context.Context, text string) {
	fmt.Printf("[内部思考]: %s\n", text)
}

func (r *TerminalReporter) OnAssistantMessage(ctx context.Context, text string) {
	fmt.Printf("[模型输出]：%s\n", text)
}

func (r *TerminalReporter) OnToolCall(ctx context.Context, call schema.ToolCall) {
	fmt.Printf("[调用工具]：%s %s\n", call.Name, string(call.Arguments))
}

func (r *TerminalReporter) OnToolResult(ctx context.Context, call schema.ToolCall, result schema.ToolResult) {
	if result.IsError {
		fmt.Printf("[工具调用失败 ERROR] %s %s\n", call.Name, result.Output)
	} else {
		fmt.Printf("[工具调用成功 OK] %s 返回 %d 字节\n", call.Name, len(result.Output))
	}
}
