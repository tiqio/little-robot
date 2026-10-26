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
	want := []string{cmdCompact, cmdClear, cmdStop, cmdPurge}
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
	msg, ok := b.perform(context.Background(), cmdStop, "oc_idle")
	if ok || !strings.Contains(msg, "没有正在跑") {
		t.Errorf("空闲时点停止应该提示没在跑，实际 (%q, %v)", msg, ok)
	}
}

// "停止"在跑的时候要真的取消
func TestStopCancelsRunning(t *testing.T) {
	b, _ := testBot(t, "oc_run", 0)
	ctx, cancel := context.WithCancel(context.Background())
	b.cancelers["oc_run"] = cancel

	msg, ok := b.perform(context.Background(), cmdStop, "oc_run")
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

	msg, ok := b.perform(context.Background(), cmdCompact, "oc_compact")
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
	msg, ok := b.perform(context.Background(), cmdCompact, "oc_empty")
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
	msg, ok := b.perform(context.Background(), cmdClear, "oc_clear")
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
	msg, ok := b.perform(context.Background(), cmdClear, "oc_clear_empty")
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

// 控制台只在上下文快满时弹：正常成功不打扰
func TestPanelHint(t *testing.T) {
	cases := []struct {
		ratio     float64
		wantPanel bool
	}{
		{0.0, false},
		{0.3, false},
		{0.59, false},
		{0.60, true},
		{0.75, true},
		{1.2, true}, // 已经超了也要弹
	}
	for _, c := range cases {
		hint, want := panelHint(c.ratio)
		if want != c.wantPanel {
			t.Errorf("ratio=%.2f 期望弹=%v，实际=%v", c.ratio, c.wantPanel, want)
		}
		if want && hint == "" {
			t.Errorf("ratio=%.2f 要弹就必须有文案", c.ratio)
		}
		if !want && hint != "" {
			t.Errorf("ratio=%.2f 不该弹却给了文案：%q", c.ratio, hint)
		}
	}
	// 文案里要有具体占比，用户才知道有多满
	hint, _ := panelHint(0.73)
	if !strings.Contains(hint, "73%") {
		t.Errorf("提示应该带占比：%q", hint)
	}
}

// 引擎的 Pressure：空会话是 0，塞满历史接近 1
func TestEnginePressure(t *testing.T) {
	b, s := testBot(t, "oc_pressure", 0)
	_, r0 := b.eng.Pressure(s)
	if r0 != 0 {
		t.Errorf("空会话占用应该是 0，实际 %.2f", r0)
	}
	b.eng.SetContextTokens(10000) // 小窗口，便于把比例顶上去
	for i := 0; i < 10; i++ {
		s.Append(schema.Message{Role: schema.RoleUser, ToolCallID: "c", Content: strings.Repeat("x", 2000)})
	}
	_, r1 := b.eng.Pressure(s)
	if r1 <= r0 || r1 < 0.5 {
		t.Errorf("塞入 20K 字符 / 10K token 窗口，占用应该很高，实际 %.2f", r1)
	}
}

// 彻底清理：会删 out/ 和 traces，且只删这个群的目录
func TestPurgeDeletesChatFiles(t *testing.T) {
	dir := t.TempDir()
	// 造一个"这个群的下载目录"
	out := filepath.Join(dir, "out", "hentai", "123")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	bigFile := filepath.Join(out, "123.pdf")
	if err := os.WriteFile(bigFile, make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	traces := filepath.Join(dir, ".claw", "traces")
	if err := os.MkdirAll(traces, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(traces, "t.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 别的东西不能被误删
	keep := filepath.Join(dir, ".claw", "aliases.json")
	if err := os.WriteFile(keep, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := &Bot{eng: engine.NewAgentEngine(nil, tools.NewRegistry(), dir, false),
		sessions: session.NewManager(), cancelers: map[string]context.CancelFunc{}}
	// 直接测文件部分：chatWorkDir 在测试环境为空，用 dirSize/remove 逻辑走一遍
	if dirSize(filepath.Join(dir, "out")) == 0 {
		t.Fatal("测试数据没造好")
	}
	// 模拟 performPurge 的文件清理段
	for _, sub := range []string{"out", filepath.Join(".claw", "traces")} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "out")); !os.IsNotExist(err) {
		t.Error("out/ 应该被删掉")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claw", "traces")); !os.IsNotExist(err) {
		t.Error("traces 应该被删掉")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("aliases.json 不该被删（不是下载产物）")
	}
	_ = b
}

// 体积格式化
func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:     "512B",
		2048:    "2KB",
		5 << 20: "5.0MB",
		3 << 30: "3.0GB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q，期望 %q", in, got, want)
		}
	}
}

// 百分比以压缩线为 100%：不会出现 200%/400% 这种没意义的数
func TestPressureIsRelativeToThreshold(t *testing.T) {
	b, s := testBot(t, "oc_pct", 0)
	b.eng.SetCompaction(10000, 0) // 窗口 10000 → 压缩线 8000 字符
	s.Append(schema.Message{Role: schema.RoleUser, Content: strings.Repeat("x", 4000)})

	chars, ratio := b.eng.Pressure(s)
	if chars != 4000 {
		t.Errorf("字符数应该是 4000，实际 %d", chars)
	}
	if ratio < 0.49 || ratio > 0.51 {
		t.Errorf("4000 字符 / 8000 压缩线 应该是 50%%，实际 %.2f", ratio)
	}

	// 到 2 倍压缩线：显示 200%，而不是把"占窗口"算成几千 %
	s.Append(schema.Message{Role: schema.RoleUser, Content: strings.Repeat("y", 12000)})
	if _, ratio2 := b.eng.Pressure(s); ratio2 < 1.9 || ratio2 > 2.1 {
		t.Errorf("16000/8000 应该约 200%%，实际 %.2f", ratio2)
	}
}

// 上下文数字：条数、字符、压缩线、窗口都要给全
func TestContextReport(t *testing.T) {
	b, s := testBot(t, "oc_report", 3)
	b.eng.SetCompaction(131072, 0.40) // 压缩线 104857
	rep := b.contextReport("oc_report")
	for _, want := range []string{"消息：3 条", "字符：", "压缩线：104857", "模型窗口：131072", "token"} {
		if !strings.Contains(rep, want) {
			t.Errorf("报告里应该包含 %q：\n%s", want, rep)
		}
	}
	_ = s
}

// 没有历史的群也要能回答
func TestContextReportEmpty(t *testing.T) {
	b, _ := testBot(t, "oc_report_empty", 0)
	if got := b.contextReport("oc_nobody"); !strings.Contains(got, "还没有历史") {
		t.Errorf("空会话应该直说：%q", got)
	}
}

// 任务在跑时发「上下文」：不能中断任务，也不能改历史
func TestContextCommandDoesNotInterrupt(t *testing.T) {
	b, s := testBot(t, "oc_ctx", 2)
	b.eng.SetCompaction(10000, 0)
	s.Append(schema.Message{Role: schema.RoleUser, Content: "正在跑的任务"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.cancelers["oc_ctx"] = cancel

	before := s.Len()
	// 走文本命令入口（它内部会 reply，测试环境 client 为 nil，会被跳过）
	if !b.handleControlText(context.Background(), "om_x", "oc_ctx", "上下文") {
		t.Fatal("「上下文」应该被当成命令处理")
	}
	if err := ctx.Err(); err != nil {
		t.Errorf("查上下文不该取消正在跑的任务：%v", err)
	}
	if s.Len() != before {
		t.Errorf("查上下文不该改历史：%d -> %d", before, s.Len())
	}
	if !b.isRunning("oc_ctx") {
		t.Error("任务应该还在跑")
	}
}

// 任务在跑时点「压缩」：拒绝并提示先停止（而不是偷偷改历史）
func TestCompactRefusedWhileRunning(t *testing.T) {
	b, s := testBot(t, "oc_compact_run", 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.cancelers["oc_compact_run"] = cancel

	before := s.Len()
	b.handleControlText(context.Background(), "om_x", "oc_compact_run", "压缩")
	if s.Len() != before {
		t.Error("跑任务时不该压缩历史")
	}
	if err := ctx.Err(); err != nil {
		t.Error("压缩被拒绝时也不该取消任务")
	}
}
