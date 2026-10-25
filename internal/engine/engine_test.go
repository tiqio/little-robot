package engine

import (
	"context"
	"strings"
	"testing"

	"little-robot/internal/schema"
	"little-robot/internal/session"
	"little-robot/internal/tools"
)

// fakeProvider 按顺序吐预设响应，用来驱动 Run 的分支
type fakeProvider struct {
	responses []schema.Message
	calls     int
}

func (f *fakeProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	idx := f.calls
	if idx >= len(f.responses) {
		idx = len(f.responses) - 1
	}
	f.calls++
	resp := f.responses[idx] // 拷贝一份，防止调用方改了切片
	return &resp, nil
}

// 空输出救援：length 截断 + 空 content + 没调工具 → 不静默退出，nudge 后再来一轮
func TestEmptyOutputNudge(t *testing.T) {
	s := session.New("test-nudge", t.TempDir())
	p := &fakeProvider{responses: []schema.Message{
		{Role: schema.RoleAssistant, Content: "", FinishReason: "length"},     // 第1轮：思考吃光额度
		{Role: schema.RoleAssistant, Content: "", FinishReason: "length"},     // 第2轮：还是没话
		{Role: schema.RoleAssistant, Content: "终于说出来了", FinishReason: "stop"}, // 第3轮：开口
	}}
	eng := NewAgentEngine(p, tools.NewRegistry(), t.TempDir(), false)

	if err := eng.Run(context.Background(), s, "帮我找个伊莉雅的本子", NewTerminalReporter()); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	if p.calls != 3 {
		t.Fatalf("应该生成 3 次（2 次 nudge + 1 次开口），实际 %d 次", p.calls)
	}

	hist := s.Messages()
	var nudges int
	for _, m := range hist {
		if strings.Contains(m.Content, "系统救援") {
			nudges++
		}
	}
	if nudges != 2 {
		t.Errorf("应该有 2 条救援消息，实际 %d 条", nudges)
	}
	last := hist[len(hist)-1]
	if last.Role != schema.RoleAssistant || last.Content != "终于说出来了" {
		t.Errorf("最后一条应该是模型的开口，实际 role=%s content=%q", last.Role, last.Content)
	}
}

// 两次救援后还是没话：给用户一句兜底话（reporter 收到），而不是干等空白
func TestSilentFallback(t *testing.T) {
	s := session.New("test-fallback", t.TempDir())
	p := &fakeProvider{responses: []schema.Message{
		{Role: schema.RoleAssistant, Content: "", FinishReason: "length"},
		{Role: schema.RoleAssistant, Content: "", FinishReason: "stop"},
		{Role: schema.RoleAssistant, Content: "", FinishReason: "length"}, // 第3次还是空 → 兜底
	}}
	eng := NewAgentEngine(p, tools.NewRegistry(), t.TempDir(), false)

	sawFallback := false
	rep := &spyReporter{onMessage: func(text string) {
		if strings.Contains(text, "没想出结果") {
			sawFallback = true
		}
	}}
	if err := eng.Run(context.Background(), s, "你好", rep); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if p.calls != 3 {
		t.Errorf("应该生成 3 次，实际 %d 次", p.calls)
	}
	if !sawFallback {
		t.Error("两次救援后应该给用户一句兜底话")
	}
}

// 正常路径不受影响：有内容或工具体现在一次就收工
func TestNoNudgeOnNormalTurn(t *testing.T) {
	s := session.New("test-normal", t.TempDir())
	p := &fakeProvider{responses: []schema.Message{
		{Role: schema.RoleAssistant, Content: "4774+997=5771", FinishReason: "stop"},
	}}
	eng := NewAgentEngine(p, tools.NewRegistry(), t.TempDir(), false)

	if err := eng.Run(context.Background(), s, "4774+997", NewTerminalReporter()); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Errorf("正常一轮应该只生成 1 次，实际 %d 次", p.calls)
	}
	if n := s.Len(); n != 2 { // user + assistant
		t.Errorf("会话应该只有 2 条，实际 %d 条", n)
	}
}

// spyReporter：记录发给用户的文本
type spyReporter struct {
	onMessage func(string)
}

func (r *spyReporter) OnThinkingStart(ctx context.Context)                  {}
func (r *spyReporter) OnThinkingText(ctx context.Context, text string)      {}
func (r *spyReporter) OnAssistantMessage(ctx context.Context, text string)  { r.onMessage(text) }
func (r *spyReporter) OnToolCall(ctx context.Context, call schema.ToolCall) {}
func (r *spyReporter) OnToolResult(ctx context.Context, call schema.ToolCall, result schema.ToolResult) {
}
func (r *spyReporter) NotifyError(ctx context.Context, err error) {}
func (r *spyReporter) NotifyBusy(ctx context.Context)             {}
