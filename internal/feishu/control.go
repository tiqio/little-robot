package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

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
	log.Printf("[Feishu] 卡片动作 chat=%s cmd=%s", chatID, cmd)
	if chatID == "" {
		return toast("error", "拿不到会话 ID"), nil
	}

	switch cmd {
	case cmdCompact:
		return b.doCompact(ctx, chatID), nil
	case cmdClear:
		return b.doClear(ctx, chatID), nil
	case cmdStop:
		return b.doStop(ctx, chatID), nil
	default:
		return toast("info", "未知操作："+cmd), nil
	}
}

// doCompact 压缩：把历史折叠成摘要，记忆还在，token 降下来
func (b *Bot) doCompact(ctx context.Context, chatID string) *callback.CardActionTriggerResponse {
	s, ok := b.sessions.Get(chatID)
	if !ok || s.Len() == 0 {
		return toast("info", "这个群还没有上下文，不用压缩")
	}
	before, after := b.eng.CompactSession(s)
	if before == after {
		return toast("info", fmt.Sprintf("已经很紧凑了（%d 字符），压不动", before))
	}
	msg := fmt.Sprintf("🧹 已压缩上下文：%d → %d 字符（省 %d%%）", before, after, pct(before, after))
	// 顺手落盘，别让重启把压缩结果丢掉
	if path, err := b.sessions.Save(s); err != nil {
		log.Printf("[Feishu] 压缩后保存失败：%v", err)
	} else if path != "" {
		log.Printf("[Feishu] 压缩后已保存：%s", path)
	}
	b.sayToChat(ctx, chatID, msg)
	return toast("success", msg)
}

// doClear 清理：历史清空 + 删掉磁盘上的旧文件，等于重新开始
func (b *Bot) doClear(ctx context.Context, chatID string) *callback.CardActionTriggerResponse {
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
		return toast("info", "上下文本来就是空的")
	}
	msg := fmt.Sprintf("🗑 已清理上下文（删掉 %d 条历史），这个群从头开始", n)
	b.sayToChat(ctx, chatID, msg)
	return toast("success", msg)
}

// doStop 停止：取消正在跑的那次 Run；没有在跑就明确告诉用户，别让人干等
func (b *Bot) doStop(ctx context.Context, chatID string) *callback.CardActionTriggerResponse {
	b.mu.Lock()
	cancel, running := b.cancelers[chatID]
	b.mu.Unlock()

	if !running || cancel == nil {
		return toast("info", "当前没有正在跑的任务")
	}
	cancel()
	log.Printf("[Feishu] 用户手动停止了 chat=%s 的任务", chatID)
	b.sayToChat(ctx, chatID, "⏹ 收到停止请求，任务会在当前步骤退出")
	return toast("success", "已发送停止信号")
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
