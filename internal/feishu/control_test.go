package feishu

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"little-robot/internal/engine"
	"little-robot/internal/schema"
	"little-robot/internal/session"
	"little-robot/internal/tools"
)

// 卡片必须是合法 JSON，且三个按钮的 value 都对
func TestControlCardJSON(t *testing.T) {
	raw := controlCardJSON("")
	if raw == "" {
		t.Fatal("卡片为空")
	}
	var card map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatalf("卡片不是合法 JSON：%v", err)
	}
	els, ok := card["elements"].([]interface{})
	if !ok || len(els) < 2 {
		t.Fatalf("卡片元素不对：%v", card["elements"])
	}

	var cmds []string
	for _, el := range els {
		m, _ := el.(map[string]interface{})
		if m["tag"] != "action" {
			continue
		}
		actions, _ := m["actions"].([]interface{})
		for _, a := range actions {
			btn, _ := a.(map[string]interface{})
			v, _ := btn["value"].(map[string]interface{})
			if cmd, _ := v["cmd"].(string); cmd != "" {
				cmds = append(cmds, cmd)
			}
		}
	}
	want := []string{cmdCompact, cmdClear, cmdStop}
	if strings.Join(cmds, ",") != strings.Join(want, ",") {
		t.Errorf("按钮 = %v，期望 %v", cmds, want)
	}
}

// testBot 造一个"有一个群、带若干历史"的 bot（不连飞书）
func testBot(t *testing.T, chatID string, history int) (*Bot, *session.Session) {
	t.Helper()
	dir := t.TempDir()
	mgr := session.NewManager()
	s := mgr.GetOrCreateAt(chatID, dir, dir)
	s.SetStoreDir(dir)
	for i := 0; i < history; i++ {
		s.Append(schema.Message{
			Role:       schema.RoleUser,
			ToolCallID: "call_" + strings.Repeat("x", i+1),
			Content:    strings.Repeat("很长的工具输出", 400),
		})
	}
	eng := engine.NewAgentEngine(nil, tools.NewRegistry(), dir, false)
	return &Bot{
		eng:       eng,
		sessions:  mgr,
		cancelers: map[string]context.CancelFunc{},
	}, s
}

// "停止"在没跑任务时只提示，不能报错
func TestStopWhenIdle(t *testing.T) {
	b, _ := testBot(t, "oc_idle", 0)
	msg, ok := b.perform(cmdStop, "oc_idle")
	if ok || !strings.Contains(msg, "没有正在跑") {
		t.Errorf("空闲时点停止应该提示没在跑，实际 (%q, %v)", msg, ok)
	}
}

// "停止"在跑的时候要真的取消
func TestStopCancelsRunning(t *testing.T) {
	b, _ := testBot(t, "oc_run", 0)
	ctx, cancel := context.WithCancel(context.Background())
	b.cancelers["oc_run"] = cancel

	msg, ok := b.perform(cmdStop, "oc_run")
	if !ok || !strings.Contains(msg, "收到停止请求") {
		t.Errorf("应该提示已发送停止信号，实际 (%q, %v)", msg, ok)
	}
	<-ctx.Done() // 取消已生效
}

// 压缩：历史真的变短，消息条数不变（只换内容不删消息）
func TestCompactButton(t *testing.T) {
	b, s := testBot(t, "oc_compact", 12)
	before := b.eng.SessionChars(s)
	if before == 0 {
		t.Fatal("压缩前应该有内容")
	}

	msg, ok := b.perform(cmdCompact, "oc_compact")
	if !ok {
		t.Fatalf("压缩应该成功，实际 %q", msg)
	}
	after := b.eng.SessionChars(s)
	if after >= before {
		t.Errorf("压缩后应该更小：%d -> %d", before, after)
	}
	if s.Len() != 12 {
		t.Errorf("压缩只换内容不删消息，条数应该不变，实际 %d", s.Len())
	}
	first := s.Messages()[0].Content
	if !strings.Contains(first, "已清理") && !strings.Contains(first, "已折叠") && !strings.Contains(first, "已截断") {
		t.Errorf("老消息应该变成占位符，实际 %q", first)
	}
	t.Logf("压缩：%d -> %d 字符，回执：%s", before, after, msg)
}

// 压缩：空历史只提示，不报错
func TestCompactEmpty(t *testing.T) {
	b, _ := testBot(t, "oc_empty", 0)
	msg, ok := b.perform(cmdCompact, "oc_empty")
	if ok || !strings.Contains(msg, "还没有上下文") {
		t.Errorf("空历史应该提示不用压缩，实际 (%q, %v)", msg, ok)
	}
}

// 清理：历史清空 + 磁盘文件删除（否则重启会"复活"）
func TestClearButton(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	mgr := session.NewManager()
	s := mgr.GetOrCreateAt("oc_clear", dir, sessDir)
	s.Append(schema.Message{Role: schema.RoleUser, Content: "你好"})
	path, err := s.Save(sessDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("会话文件应该存在：%v", err)
	}

	b := &Bot{eng: engine.NewAgentEngine(nil, tools.NewRegistry(), dir, false), sessions: mgr, cancelers: map[string]context.CancelFunc{}}
	msg, ok := b.perform(cmdClear, "oc_clear")
	if !ok {
		t.Fatalf("清理应该成功，实际 %q", msg)
	}
	if s.Len() != 0 {
		t.Errorf("历史应该清空，实际 %d 条", s.Len())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("磁盘上的会话文件应该被删掉（不然重启会恢复旧上下文）")
	}
}

// 清理：本来就是空的，只提示
func TestClearEmpty(t *testing.T) {
	b, _ := testBot(t, "oc_clear_empty", 0)
	msg, ok := b.perform(cmdClear, "oc_clear_empty")
	if ok || !strings.Contains(msg, "本来就是空") {
		t.Errorf("空历史应该提示，实际 (%q, %v)", msg, ok)
	}
}

// 文本命令解析：常用词都能认，普通话不算
func TestControlCommandOf(t *testing.T) {
	cases := map[string]string{
		"压缩": cmdCompact, "压缩上下文": cmdCompact, "/compact": cmdCompact,
		"清理": cmdClear, "清空上下文": cmdClear, "／clear": cmdClear, // 全角斜杠
		"停止": cmdStop, "停": cmdStop, "/stop": cmdStop,
		"面板": "menu", "控制台": "menu", "菜单": "menu",
	}
	for in, want := range cases {
		if got := controlCommandOf(in); got != want {
			t.Errorf("controlCommandOf(%q) = %q，期望 %q", in, got, want)
		}
	}
	for _, in := range []string{"帮我画个面板", "抓本子", "你好", "停止一下好不好"} {
		if got := controlCommandOf(in); got != "" {
			t.Errorf("%q 不该被当成命令（会误吞正常请求），实际 %q", in, got)
		}
	}
}

// 自检：回调没收到过时要明确指出去后台开通，并给文本命令兜底
func TestDiagnosisWhenNoCallback(t *testing.T) {
	b, _ := testBot(t, "oc_diag", 3)
	out := b.diagnosis("oc_diag")
	if !strings.Contains(out, "一次都没收到过") {
		t.Errorf("没收到回调时应该提示：%s", out)
	}
	if !strings.Contains(out, "压缩") || !strings.Contains(out, "停止") {
		t.Errorf("应该给出文本命令兜底：%s", out)
	}
	if !strings.Contains(out, "3 条") {
		t.Errorf("应该报告上下文条数：%s", out)
	}
}

// 自检：收到过回调就显示 ✅ 和次数
func TestDiagnosisWithCallback(t *testing.T) {
	b, _ := testBot(t, "oc_diag2", 0)
	b.cardCallbacks.Add(2)
	out := b.diagnosis("oc_diag2")
	if !strings.Contains(out, "已收到 2 次") {
		t.Errorf("应该显示回调次数：%s", out)
	}
}

// 自检命令能被解析
func TestDiagnosisCommand(t *testing.T) {
	if got := controlCommandOf("自检"); got != "doctor" {
		t.Errorf("「自检」应该映射到 doctor，实际 %q", got)
	}
}
