package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"little-robot/internal/provider"
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

// recordingProvider 记录每次 Generate：有没有工具、当时的预算、历史尾巴
type recordingProvider struct {
	responses []schema.Message
	calls     []callRecord
}

type callRecord struct {
	toolCount int
	maxTokens int
	lastMsg   schema.Message
}

func (f *recordingProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	idx := len(f.calls)
	if idx >= len(f.responses) {
		idx = len(f.responses) - 1
	}
	f.calls = append(f.calls, callRecord{
		toolCount: len(availableTools),
		maxTokens: provider.MaxTokensFrom(ctx, 0),
		lastMsg:   msgs[len(msgs)-1],
	})
	resp := f.responses[idx]
	return &resp, nil
}

// 思考/行动分家（S2）：第1次调用=思考段（无工具），之后每次=行动段（有工具），
// 计划注入行动段历史，且每个 Run 只思考一次
func TestThinkOnceThenAct(t *testing.T) {
	workDir := t.TempDir()
	reg := tools.NewRegistry()
	reg.Register(tools.NewBashTool(workDir))
	s := session.New("test-think", workDir)

	bashCall := schema.ToolCall{ID: "call_1", Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`)}
	p := &recordingProvider{responses: []schema.Message{
		{Role: schema.RoleAssistant, Content: "计划：跑一下 echo hi", FinishReason: "stop"},                                 // 思考段
		{Role: schema.RoleAssistant, Content: "", ToolCalls: []schema.ToolCall{bashCall}, FinishReason: "tool_calls"}, // 行动段1
		{Role: schema.RoleAssistant, Content: "搞定", FinishReason: "stop"},                                             // 行动段2（不再思考）
	}}
	eng := NewAgentEngine(p, reg, workDir, true)
	eng.SetThinkingBudgets(2048, 1024)

	if err := eng.Run(context.Background(), s, "跑个命令", NewTerminalReporter()); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	if len(p.calls) != 3 {
		t.Fatalf("应该 3 次调用（1 思考 + 2 行动），实际 %d 次", len(p.calls))
	}
	// 第1次：思考段，没有工具，预算 2048
	if p.calls[0].toolCount != 0 {
		t.Errorf("思考段应该有 0 个工具，实际 %d", p.calls[0].toolCount)
	}
	if p.calls[0].maxTokens != 2048 {
		t.Errorf("思考段预算 = %d，期望 2048", p.calls[0].maxTokens)
	}
	// 第2次：行动段，有工具，预算 1024，历史末尾是注入的计划
	if p.calls[1].toolCount == 0 {
		t.Error("行动段应该有工具")
	}
	if p.calls[1].maxTokens != 1024 {
		t.Errorf("行动段预算 = %d，期望 1024", p.calls[1].maxTokens)
	}
	if p.calls[1].lastMsg.Role != schema.RoleAssistant || p.calls[1].lastMsg.Content != "计划：跑一下 echo hi" {
		t.Errorf("行动段历史末尾应该是注入的计划，实际 role=%s content=%q", p.calls[1].lastMsg.Role, p.calls[1].lastMsg.Content)
	}
	// 第3次：还是行动段（本 Run 不再思考）
	if p.calls[2].toolCount == 0 {
		t.Error("第3次应该仍是行动段（有工具）")
	}
}

// rejectingProvider 第一次带图就报"拒图"，剥图后成功：模拟小 webp 事故
type rejectingProvider struct {
	calls     int
	sawImages []bool // 每次调用时历史里还有没有图
}

func (f *rejectingProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	f.calls++
	hasImage := false
	for _, m := range msgs {
		if len(m.Images) > 0 {
			hasImage = true
		}
	}
	f.sawImages = append(f.sawImages, hasImage)

	if hasImage {
		return nil, fmt.Errorf("%s 不接受这次请求里的图片：%w", "lmstudio", provider.ErrImageRejected)
	}
	return &schema.Message{Role: schema.RoleAssistant, Content: "图没看成，但我用文字答了", FinishReason: "stop"}, nil
}

// 拒图降级：带图请求失败 → 剥图重试一次 → 任务继续，用户拿到答案而不是 error
func TestImageRejectedFallsBack(t *testing.T) {
	s := session.New("test-img-fallback", t.TempDir())
	// 会话里有一张图（模拟刚抓回来的图已经进历史）
	s.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: "看看这张图",
		Images:  []string{"data:image/webp;base64,AAAA"},
	})

	p := &rejectingProvider{}
	eng := NewAgentEngine(p, tools.NewRegistry(), t.TempDir(), false)

	var got string
	rep := &spyReporter{onMessage: func(text string) { got = text }}
	if err := eng.Run(context.Background(), s, "这张图里有什么", rep); err != nil {
		t.Fatalf("拒图应该降级而不是报错：%v", err)
	}

	if p.calls != 2 {
		t.Fatalf("应该 2 次调用（带图失败 + 剥图重试），实际 %d 次", p.calls)
	}
	if !p.sawImages[0] {
		t.Error("第1次调用应该带图")
	}
	if p.sawImages[1] {
		t.Error("第2次调用必须已经剥掉图")
	}
	if !strings.Contains(got, "文字答了") {
		t.Errorf("重试成功后应该把答案发给用户，实际 %q", got)
	}
}

// overflowProvider 第一次报"上下文超限"，压缩后（历史变短）就成功
type overflowProvider struct {
	calls int
	sizes []int
}

func (f *overflowProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
	}
	f.calls++
	f.sizes = append(f.sizes, n)
	if f.calls == 1 {
		return nil, fmt.Errorf("lmstudio 说请求超出上下文窗口：%w", provider.ErrContextOverflow)
	}
	return &schema.Message{Role: schema.RoleAssistant, Content: "压完就答出来了", FinishReason: "stop"}, nil
}

// 上下文超限：自动压缩并重试一次，用户仍然拿到答案
func TestContextOverflowRecovers(t *testing.T) {
	dir := t.TempDir()
	s := session.New("test-overflow", dir)
	for i := 0; i < 12; i++ {
		s.Append(schema.Message{
			Role:       schema.RoleUser,
			ToolCallID: "call_x",
			Content:    strings.Repeat("很早以前的工具输出", 500),
		})
	}

	p := &overflowProvider{}
	eng := NewAgentEngine(p, tools.NewRegistry(), dir, false)

	var got string
	rep := &spyReporter{onMessage: func(text string) { got = text }}
	if err := eng.Run(context.Background(), s, "继续", rep); err != nil {
		t.Fatalf("超限应该自救而不是报错：%v", err)
	}

	if p.calls != 2 {
		t.Fatalf("应该 2 次调用（超限 + 压缩后重试），实际 %d", p.calls)
	}
	// 重试用的历史不能变长（会话已经在"就地压缩"那一步变小了，这里允许相等）
	if p.sizes[1] > p.sizes[0] {
		t.Errorf("重试时的历史不该变大：%d -> %d", p.sizes[0], p.sizes[1])
	}
	if !strings.Contains(got, "压完就答出来了") {
		t.Errorf("重试成功后应该把答案发给用户，实际 %q", got)
	}
}

// 会话超过压缩线要就地压缩：数字会真的降下来，而不是只压发出去的快照
func TestSessionCompactedInPlace(t *testing.T) {
	dir := t.TempDir()
	s := session.New("test-inplace", dir)
	// 塞到超过压缩线（窗口 10000 → 压缩线 8000 字符）
	for i := 0; i < 20; i++ {
		s.Append(schema.Message{Role: schema.RoleUser, ToolCallID: "c", Content: strings.Repeat("旧结果", 500)})
	}
	p := &fakeProvider{responses: []schema.Message{{Role: schema.RoleAssistant, Content: "好", FinishReason: "stop"}}}
	eng := NewAgentEngine(p, tools.NewRegistry(), dir, false)
	eng.SetCompaction(10000, 0) // 压缩线 8000

	before := eng.Stats(s).Chars
	if before < eng.CompactionThreshold() {
		t.Fatalf("测试数据应该超过压缩线：%d < %d", before, eng.CompactionThreshold())
	}

	if err := eng.Run(context.Background(), s, "继续", &spyReporter{onMessage: func(string) {}}); err != nil {
		t.Fatal(err)
	}

	after := eng.Stats(s).Chars
	if after >= before {
		t.Errorf("会话应该被就地压缩：%d -> %d", before, after)
	}
	if after >= eng.CompactionThreshold() {
		t.Errorf("压缩后应该低于压缩线（%d），实际 %d", eng.CompactionThreshold(), after)
	}
	t.Logf("会话就地压缩：%d -> %d 字符（压缩线 %d）", before, after, eng.CompactionThreshold())
}

// 跑完必须把会话压回线下：否则面板会报"282%"（本次运行又注入了图片）
func TestSessionCompactedAfterRun(t *testing.T) {
	dir := t.TempDir()
	s := session.New("test-after", dir)
	p := &fakeProvider{responses: []schema.Message{{Role: schema.RoleAssistant, Content: "好", FinishReason: "stop"}}}
	eng := NewAgentEngine(p, tools.NewRegistry(), dir, false)
	eng.SetCompaction(10000, 0) // 压缩线 8000

	// 模拟"这次运行注入了图片"：直接往会话里塞大消息
	s.Append(schema.Message{Role: schema.RoleUser, Content: strings.Repeat("x", 30000)})

	if err := eng.Run(context.Background(), s, "继续", &spyReporter{onMessage: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if got := eng.Stats(s).Chars; got >= eng.CompactionThreshold() {
		t.Errorf("跑完后会话应该被压回线下（%d 字符，线 %d）", got, eng.CompactionThreshold())
	}
}
