package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"little-robot/internal/session"
)

// 卡片按钮的三种动作
const (
	cmdCompact = "compact" // 压缩上下文
	cmdClear   = "clear"   // 清理上下文
	cmdStop    = "stop"    // 停止正在跑的 Run
)

// controlCardJSON 三键控制卡片。
//
// 为什么做成卡片：手机上打字费劲，"压缩/清理/停止"是三个固定动作，
// 点在屏幕上比记命令容易；每轮任务结束后跟一张，随时可点。
func controlCardJSON(hint string) string {
	if hint == "" {
		hint = "**压缩**：把历史折叠成摘要（省 token，保留记忆）\n" +
			"**清理**：删掉这个群的全部上下文（重新开始）\n" +
			"**停止**：中断正在跑的任务"
	}
	card := map[string]interface{}{
		"config": map[string]interface{}{"wide_screen_mode": true},
		"header": map[string]interface{}{
			"title":    map[string]interface{}{"tag": "plain_text", "content": "🎛 机器人控制台"},
			"template": "blue",
		},
		"elements": []interface{}{
			map[string]interface{}{
				"tag":  "div",
				"text": map[string]interface{}{"tag": "lark_md", "content": hint},
			},
			map[string]interface{}{
				"tag": "action",
				"actions": []interface{}{
					buttonJSON("🧹 压缩上下文", "default", cmdCompact),
					buttonJSON("🗑 清理上下文", "danger", cmdClear),
					buttonJSON("⏹ 停止任务", "primary", cmdStop),
				},
			},
		},
	}
	b, err := json.Marshal(card)
	if err != nil {
		log.Printf("[Feishu] 生成控制卡片失败：%v", err)
		return ""
	}
	return string(b)
}

func buttonJSON(text, typ, cmd string) map[string]interface{} {
	return map[string]interface{}{
		"tag":  "button",
		"text": map[string]interface{}{"tag": "plain_text", "content": text},
		"type": typ,
		// value 会原样回到回调的 action.value 里
		"value": map[string]interface{}{"cmd": cmd},
	}
}

// onCardAction 处理卡片按钮回调。
// 返回的 Toast 是"只对点击者可见"的小提示——正好用来回答"没在跑"这种情况。
func (b *Bot) onCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil {
		return toast("error", "没读懂这次点击"), nil
	}
	ev := event.Event
	chatID := ""
	if ev.Context != nil {
		chatID = ev.Context.OpenChatID
	}
	cmd := ""
	if ev.Action != nil {
		if v, ok := ev.Action.Value["cmd"].(string); ok {
			cmd = v
		}
	}
	b.cardCallbacks.Add(1)
	log.Printf("[Feishu] 卡片动作 chat=%s cmd=%s", chatID, cmd)
	if chatID == "" {
		return toast("error", "拿不到会话 ID"), nil
	}

	msg, ok := b.perform(cmd, chatID)
	if cmd != cmdStop {
		// 压缩/清理是共享状态变更，群里也留一条（toast 只有点的人看得见）
		b.sayToChat(ctx, chatID, msg)
	}
	if ok {
		return toast("success", msg), nil
	}
	return toast("info", msg), nil
}

// perform 执行一个控制动作，返回（给用户看的话, 是否真的做了什么）。
// 卡片按钮和文本命令都走这里：一份实现，两个入口，不会出现"点了没反应但打字有效"。
func (b *Bot) perform(cmd, chatID string) (string, bool) {
	switch cmd {
	case cmdCompact:
		return b.performCompact(chatID)
	case cmdClear:
		return b.performClear(chatID)
	case cmdStop:
		return b.performStop(chatID)
	}
	return "未知操作：" + cmd, false
}

// performCompact 压缩：把历史折叠成摘要，记忆还在，token 降下来
func (b *Bot) performCompact(chatID string) (string, bool) {
	s, ok := b.sessions.Get(chatID)
	if !ok || s.Len() == 0 {
		return "这个群还没有上下文，不用压缩", false
	}
	before, after := b.eng.CompactSession(s)
	if before == after {
		return fmt.Sprintf("已经很紧凑了（%d 字符），压不动", before), false
	}
	// 顺手落盘，别让重启把压缩结果丢掉
	if path, err := b.sessions.Save(s); err != nil {
		log.Printf("[Feishu] 压缩后保存失败：%v", err)
	} else if path != "" {
		log.Printf("[Feishu] 压缩后已保存：%s", path)
	}
	return fmt.Sprintf("🧹 已压缩上下文：%d → %d 字符（省 %d%%）", before, after, pct(before, after)), true
}

// performClear 清理：历史清空 + 删掉磁盘上的旧文件，等于重新开始
func (b *Bot) performClear(chatID string) (string, bool) {
	s, ok := b.sessions.Get(chatID)
	n := 0
	dir := ""
	if ok {
		n = s.Len()
		dir = s.StoreDir()
		s.Reset()
	}
	// 磁盘上的旧历史也要删：不然重启后又"复活"了
	if dir != "" {
		path := filepath.Join(dir, session.SafeName(chatID)+".json")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[Feishu] 删除会话文件失败：%v", err)
		}
	}
	if n == 0 {
		return "上下文本来就是空的", false
	}
	return fmt.Sprintf("🗑 已清理上下文（删掉 %d 条历史），这个群从头开始", n), true
}

// performStop 停止：取消正在跑的那次 Run；没有在跑就明确告诉用户，别让人干等
func (b *Bot) performStop(chatID string) (string, bool) {
	b.mu.Lock()
	cancel, running := b.cancelers[chatID]
	b.mu.Unlock()

	if !running || cancel == nil {
		return "当前没有正在跑的任务", false
	}
	cancel()
	log.Printf("[Feishu] 用户手动停止了 chat=%s 的任务", chatID)
	return "⏹ 收到停止请求，任务会在当前步骤退出", true
}

// isRunning 这个群现在有没有任务在跑（压缩/清理前要检查，别把跑着的会话改了）
func (b *Bot) isRunning(chatID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.cancelers[chatID]
	return ok
}

// sayToChat 主动往群里发一条文本（卡片回调没有"回复某条消息"的上下文，走 chat_id 发送）
func (b *Bot) sayToChat(ctx context.Context, chatID, text string) {
	if b.client == nil { // 单测里没有飞书客户端，跳过发送
		return
	}
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		log.Printf("[Feishu] 序列化消息失败：%v", err)
		return
	}
	body := larkim.NewCreateMessageReqBodyBuilder().
		ReceiveId(chatID).
		MsgType("text").
		Content(string(content)).
		Build()
	req := larkim.NewCreateMessageReqBuilder().Body(body).Build()
	if _, err := b.client.Im.Message.Create(ctx, req); err != nil {
		log.Printf("[Feishu] 发消息到群失败：%v", err)
	}
}

// replyCard 用卡片回复某条消息（控制台面板）
func (r *FeishuReporter) replyCard(ctx context.Context, cardJSON string) error {
	// interactive 卡片的 content 就是卡片结构本身，不用 {"text": ...} 包装
	body := larkim.NewReplyMessageReqBodyBuilder().
		MsgType("interactive").
		Content(cardJSON).
		Build()
	req := larkim.NewReplyMessageReqBuilder().MessageId(r.messageID).Body(body).Build()
	_, err := r.client.Im.Message.Reply(ctx, req)
	return err
}

// toast 只给点击者看的小提示
func toast(typ, content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: typ, Content: content},
	}
}

// pct 计算省下的百分比
func pct(before, after int) int {
	if before <= 0 || after >= before {
		return 0
	}
	return (before - after) * 100 / before
}

// ---- 文本命令：卡片回调没开通时的兜底，也是手机上最快的方式 ----

// controlCommandOf 把一句话解析成控制命令；不是命令返回空串。
// 用词要短、要好记：手机上是"打两个字"而不是"念一段话"。
func controlCommandOf(text string) string {
	t := strings.TrimSpace(text)
	// 全角斜杠也认（中文输入法默认打出来的就是／）
	t = strings.ReplaceAll(t, "／", "/")
	switch strings.ToLower(t) {
	case "压缩", "压缩上下文", "/compact", "compact", "压一下":
		return cmdCompact
	case "清理", "清理上下文", "清空", "清空上下文", "/clear", "clear":
		return cmdClear
	case "停止", "停下", "停", "中断", "/stop", "stop":
		return cmdStop
	case "面板", "控制台", "控制面板", "按钮", "菜单", "/menu", "menu":
		return "menu"
	case "自检", "体检", "状态", "/doctor", "doctor", "/status":
		return "doctor"
	}
	return ""
}

// handleControlText 处理文本控制命令；返回 true = 这条消息已被消费，不该再进模型。
// 关键点：这个分支在抢锁之前处理，所以任务正在跑时"停止"照样生效。
func (b *Bot) handleControlText(ctx context.Context, messageID, chatID, text string) bool {
	cmd := controlCommandOf(text)
	if cmd == "" {
		return false
	}
	reporter := NewFeishuReporter(b.client, messageID)

	if cmd == "doctor" {
		_ = reporter.reply(ctx, b.diagnosis(chatID))
		return true
	}

	if cmd == "menu" {
		if card := controlCardJSON(""); card != "" {
			if err := reporter.replyCard(ctx, card); err != nil {
				log.Printf("[Feishu] 发送控制台卡片失败：%v", err)
				_ = reporter.reply(ctx, "卡片发不出去（可能是卡片回调没开通）：\n"+
					"直接发「压缩」「清理」「停止」也能用。错误："+err.Error())
			}
		}
		return true
	}

	// 压缩/清理会改历史，跑着的时候先让用户停
	if cmd != cmdStop && b.isRunning(chatID) {
		_ = reporter.reply(ctx, "任务正在跑，先发「停止」再"+map[string]string{
			cmdCompact: "压缩", cmdClear: "清理",
		}[cmd])
		return true
	}

	msg, ok := b.perform(cmd, chatID)
	out := msg
	if !ok {
		out = "（不用做）" + msg
	}
	if err := reporter.reply(ctx, out); err != nil {
		log.Printf("[Feishu] 控制命令回复失败：%v", err)
	}
	return true
}

// diagnosis 自检：一眼看出"卡片能不能用""上下文多大""有没有在跑"。
// 为什么要有：卡片回调是飞书后台的开关，代码这边看不出来，
// 用户点按钮没反应时，第一条要排查的就是"回调事件到底有没有到"。
func (b *Bot) diagnosis(chatID string) string {
	var sb strings.Builder
	sb.WriteString("🩺 自检\n")

	n := b.cardCallbacks.Load()
	if n == 0 {
		sb.WriteString("卡片回调：❌ 一次都没收到过\n")
		sb.WriteString("  → 点按钮没反应的话，去飞书后台「事件与回调 → 回调配置」\n")
		sb.WriteString("     选「长连接」并添加 card.action.trigger\n")
		sb.WriteString("  → 在此之前用文本命令：压缩 / 清理 / 停止\n")
	} else {
		sb.WriteString(fmt.Sprintf("卡片回调：✅ 已收到 %d 次\n", n))
	}

	if s, ok := b.sessions.Get(chatID); ok {
		sb.WriteString(fmt.Sprintf("上下文：%d 条 / 约 %d 字符\n", s.Len(), b.eng.SessionChars(s)))
	} else {
		sb.WriteString("上下文：这个群还没有历史\n")
	}

	if b.cancelers[chatID] != nil {
		sb.WriteString("当前任务：▶ 正在跑（可以发「停止」）\n")
	} else {
		sb.WriteString("当前任务：空闲\n")
	}
	sb.WriteString(fmt.Sprintf("单次超时：%s（RUN_TIMEOUT_MIN 可调）", runTimeout()))
	return sb.String()
}
