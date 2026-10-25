package feishu

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"little-robot/internal/schema"
)

// recordingReporter 把 sink 换成记录器：不碰 lark 网络，只记录发出的文本
func recordingReporter(t *testing.T) (*FeishuReporter, *[]string) {
	t.Helper()
	var out []string
	r := &FeishuReporter{
		messageID: "om_test",
		sink: func(ctx context.Context, text string) error {
			out = append(out, text)
			return nil
		},
	}
	return r, &out
}

// doneMessage：有工具调用时带上次数，没有就只报耗时
func TestDoneMessage(t *testing.T) {
	if got := doneMessage(45*time.Second, 0); got != "✅ 完成（用时45s）" {
		t.Errorf("无工具：%q", got)
	}
	if got := doneMessage(62*time.Second, 3); got != "✅ 完成（3 次工具调用 · 用时1m02s）" {
		t.Errorf("有工具：%q", got)
	}
}

// humanDur：<1s / Ns / NmNNs 三档
func TestHumanDur(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{500 * time.Millisecond, "<1s"},
		{time.Second, "1s"},
		{47 * time.Second, "47s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m00s"},
		{62 * time.Second, "1m02s"},
		{3*time.Minute + 7*time.Second, "3m07s"},
	}
	for _, c := range cases {
		if got := humanDur(c.in); got != c.want {
			t.Errorf("humanDur(%v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 完整事件流：开始 → 思考 → 工具×3 → 答案 → 完成（带工具次数）
// 这就是用户手机上看到的一条完整任务时间线
func TestFullEventStream(t *testing.T) {
	r, out := recordingReporter(t)
	ctx := context.Background()

	r.NotifyStart(ctx)
	r.OnThinkingStart(ctx)
	r.OnToolCall(ctx, schema.ToolCall{Name: "fetch_image"})
	r.OnToolCall(ctx, schema.ToolCall{Name: "bash"})
	r.OnToolCall(ctx, schema.ToolCall{Name: "send"})
	r.OnAssistantMessage(ctx, "这是你要的图")
	r.NotifyDone(ctx, 47*time.Second)

	got := strings.Join(*out, "\n")
	wantOrder := []string{
		startMessage(),
		"💭 思考中…",
		"[正在执行] fetch_image",
		"[正在执行] bash",
		"[正在执行] send",
		"这是你要的图",
		"✅ 完成（3 次工具调用 · 用时47s）",
	}
	for i, w := range wantOrder {
		if i >= len(*out) {
			t.Fatalf("消息不够：只发了 %d 条\n%s", len(*out), got)
		}
		if (*out)[i] != w {
			t.Errorf("第 %d 条 = %q，期望 %q", i+1, (*out)[i], w)
		}
	}
	if len(*out) != len(wantOrder) {
		t.Errorf("应该正好 %d 条，实际 %d 条", len(wantOrder), len(*out))
	}
}

// 没调工具的任务：完成标记不带次数
func TestDoneWithoutTools(t *testing.T) {
	r, out := recordingReporter(t)
	r.NotifyStart(context.Background())
	r.OnAssistantMessage(context.Background(), "4774+997=5771")
	r.NotifyDone(context.Background(), 2*time.Second)

	if len(*out) != 3 || !strings.HasSuffix((*out)[2], "✅ 完成（用时2s）") {
		t.Errorf("事件流不对：%v", *out)
	}
}

// 忙碌：文案要指向 ✅（用户据此判断什么时候能重发）
func TestBusyMessage(t *testing.T) {
	_, out := recordingReporter(t)
	// 直接校验纯函数（send 的包装在上面已经覆盖过）
	if !strings.Contains(busyMessage(), "✅") {
		t.Errorf("busy 文案应该告诉用户等 ✅：%q", busyMessage())
	}
	_ = out
}

// 中断也是边界：带耗时和原因
func TestErrorMessage(t *testing.T) {
	r, out := recordingReporter(t)
	r.NotifyError(context.Background(), fmt.Errorf("context deadline exceeded"), 10*time.Minute)

	if len(*out) != 1 {
		t.Fatalf("应该只发一条：%v", *out)
	}
	if !strings.Contains((*out)[0], "⚠️ 任务中断") || !strings.Contains((*out)[0], "10m00s") {
		t.Errorf("中断文案缺关键信息：%q", (*out)[0])
	}
}
