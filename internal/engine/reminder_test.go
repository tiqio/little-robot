package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"little-robot/internal/schema"
)

func call(args string) schema.ToolCall {
	return schema.ToolCall{Name: "fetch_hentai", Arguments: json.RawMessage(args)}
}

// 关键回归：结果**成功**（0 条）但参数不变，也要能被检测到。
// 旧实现只在 IsError 时计数，真实事故里烧了 82 轮。
func TestReminderCatchesSuccessfulRepeat(t *testing.T) {
	r := NewReminder()
	ok := []schema.ToolResult{{IsError: false, Output: "0 条结果"}}

	var hinted bool
	var aborted bool
	for i := 0; i < 6; i++ {
		nudge, abort := r.Observe([]schema.ToolCall{call(`{"query":"aysa wuthering_waves chinese"}`)}, ok)
		if nudge != nil {
			hinted = true
		}
		if abort {
			aborted = true
			break
		}
	}
	if !hinted {
		t.Error("连续 3 次同参数就该提示换策略")
	}
	if !aborted {
		t.Error("连续 6 次同参数应该强制停下")
	}
}

// 换了参数就算"在往前走"，计数要重置
func TestReminderResetsOnDifferentArgs(t *testing.T) {
	r := NewReminder()
	ok := []schema.ToolResult{{IsError: false}}
	for i := 0; i < 4; i++ {
		// 每次参数都不同
		_, abort := r.Observe([]schema.ToolCall{call(`{"query":"q` + string(rune('a'+i)) + `"}`)}, ok)
		if abort {
			t.Fatal("参数一直在变，不该判定打转")
		}
	}
	if r.Streak() != 1 {
		t.Errorf("换参数后连续计数应该重置为 1，实际 %d", r.Streak())
	}
}

// 停止时给用户的话要能看懂发生了什么
func TestAbortMessage(t *testing.T) {
	msg := AbortMessage("fetch_hentai", 6)
	if !strings.Contains(msg, "fetch_hentai") || !strings.Contains(msg, "6 次") {
		t.Errorf("终止文案不清楚：%s", msg)
	}
}
