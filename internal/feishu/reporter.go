package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"little-robot/internal/engine"
	"little-robot/internal/schema"
	"little-robot/internal/tools"
	"log"
	"os"
	"sync/atomic"
	"time"
)

var _ engine.Reporter = &FeishuReporter{}

// FeishuReporter 把引擎事件推回飞书，回复到出发它的那条消息上
type FeishuReporter struct {
	client    *lark.Client
	messageID string // 没有chatID，会通过MessageId(r.messageID)恢复某条消息，飞书会自动把这条回复投递到那条消息所在的会话，不需要告诉它chat_id
	// chat_id只在bot.go里面用在和发送无关的两个地方：
	// 1. b.sessions.GetOrCreate(chatID, b.eng.WorkDir) 会话记忆按chat分文件
	// 2. lock := b.lockFor(chatID) 同一个群串行执行
	// bot管这次运行属于哪个会话，reporter管这条回复挂在哪条消息下面
	// sink 发送出口：生产是 reply（lark API），测试换成记录器就不碰网络了
	sink func(ctx context.Context, text string) error
	// toolCalls 工具调用计数：OnToolCall 在并行 goroutine 里触发，用 atomic 防竞争。
	// 完成标记里带上它，用户手机上能知道"这次任务干了多大事"。
	toolCalls atomic.Int64
}

func NewFeishuReporter(client *lark.Client, messageID string) *FeishuReporter {
	r := &FeishuReporter{client: client, messageID: messageID}
	r.sink = r.reply
	return r
}

// OnThinkingStart 思考型模型开始"规划段"时喊一声：
// 之前思考能跑 74 秒，用户在飞书端一片空白、以为 bot 死了，直接放弃。
// 现在先回一句"💭 思考中"，把等待变成"它在想"，体感完全不同。
func (r *FeishuReporter) OnThinkingStart(ctx context.Context) {
	r.send(ctx, "💭 思考中…")
}

func (r *FeishuReporter) OnThinkingText(ctx context.Context, text string) {}

func (r *FeishuReporter) OnAssistantMessage(ctx context.Context, text string) {
	r.send(ctx, text)
}

func (r *FeishuReporter) OnToolCall(ctx context.Context, call schema.ToolCall) {
	r.toolCalls.Add(1)
	r.send(ctx, "[正在执行] "+call.Name)
}

func (r *FeishuReporter) OnToolResult(ctx context.Context, call schema.ToolCall, result schema.ToolResult) {
}

// 飞书单条消息有长度上限，按照rune切段，避免把中文劈坏
func (r *FeishuReporter) send(ctx context.Context, text string) {
	const chunk = 3000
	runes := []rune(text)
	for len(runes) > 0 {
		n := chunk
		if n > len(runes) {
			n = len(runes)
		}
		if err := r.sink(ctx, string(runes[:n])); err != nil {
			log.Printf("[Feishu] 回复失败：%v", err)
			return
		}
		runes = runes[n:]
	}
}

func (r *FeishuReporter) reply(ctx context.Context, text string) error {
	if r.client == nil { // 单测里没有飞书客户端
		return nil
	}
	// 用json.Marshal构造content，而不是larkim.NewTextMsgBuilder()
	// SDK的Text()是裸字符串凭借，不转义引号和反斜杠，输出带"就会变成非法JSON
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}

	body := larkim.NewReplyMessageReqBodyBuilder().MsgType("text").Content(string(content)).Build()
	req := larkim.NewReplyMessageReqBuilder().MessageId(r.messageID).Body(body).Build()
	_, err = r.client.Im.Message.Reply(ctx, req)
	return err
}

// ---- 任务边界标记（ch35）----
// 手机上最焦虑的是"它到底跑完没有"：跑之前不知道开始了，跑完不知道结束了，
// 于是抢着发下一条消息，撞上"上一个任务还在跑"。三个标记把任务的边界画出来。

// NotifyStart 拿到锁、真正开跑时发：用户在手机上立刻看到"收到了，开始干"
func (r *FeishuReporter) NotifyStart(ctx context.Context) {
	r.send(ctx, startMessage())
}

// NotifyDone 跑完（无论调没调工具）发：用户看到 ✅ 就知道"上一条结束了，可以发下一条"
func (r *FeishuReporter) NotifyDone(ctx context.Context, elapsed time.Duration) {
	r.send(ctx, doneMessage(elapsed, r.toolCalls.Load()))
}

// NotifyError 任务中途挂掉（超时/模型报错）发：也是边界，不然是"死给你看"
func (r *FeishuReporter) NotifyError(ctx context.Context, err error, elapsed time.Duration) {
	r.send(ctx, fmt.Sprintf("⚠️ 任务中断（用时%s）：%v", humanDur(elapsed), err))
}

// NotifyBusy 任务还在跑时又来一条：现在的语义是"这条先放下"（bot 直接丢），
// 文案把"什么时候能重发"说清楚——等看到 ✅
func (r *FeishuReporter) NotifyBusy(ctx context.Context) {
	r.send(ctx, busyMessage())
}

// 边界文案抽成纯函数：单测测的就是真正发出去的文本
func startMessage() string { return "🚀 开始处理…" }

func busyMessage() string { return "⏳ 上一个任务还在跑，看到 ✅ 再重发这条" }

// doneMessage 纯函数，方便单测
func doneMessage(elapsed time.Duration, toolCalls int64) string {
	if toolCalls > 0 {
		return fmt.Sprintf("✅ 完成（%d 次工具调用 · 用时%s）", toolCalls, humanDur(elapsed))
	}
	return fmt.Sprintf("✅ 完成（用时%s）", humanDur(elapsed))
}

// humanDur 人类可读的耗时：<1s / 47s / 1m02s
func humanDur(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

var _ tools.FileSender = &FeishuReporter{}

// SendFile 实现 tools.FileSender：先上传换 file_key，再用 file 消息回复。
// fileType 是飞书的枚举（pdf/mp4/doc/xls/ppt/opus/stream），由 send 工具推断好传进来。
func (r *FeishuReporter) SendFile(ctx context.Context, absPath, fileName, fileType string) error {
	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("打开文件失败：%w", err)
	}
	defer f.Close()

	resp, err := r.client.Im.File.Create(ctx, larkim.NewCreateFileReqBuilder().
		Body(larkim.NewCreateFileReqBodyBuilder().
			FileType(fileType).
			FileName(fileName).
			File(f).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("上传文件失败：%w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("上传文件被拒绝：code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.FileKey == nil {
		return fmt.Errorf("上传成功但没有返回 file_key")
	}

	content, err := json.Marshal(map[string]string{"file_key": *resp.Data.FileKey})
	if err != nil {
		return fmt.Errorf("序列化文件消息失败：%w", err)
	}

	body := larkim.NewReplyMessageReqBodyBuilder().
		MsgType("file").
		Content(string(content)).
		Build()
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(r.messageID).
		Body(body).
		Build()

	if _, err := r.client.Im.Message.Reply(ctx, req); err != nil {
		return fmt.Errorf("发送文件消息失败：%w", err)
	}
	return nil
}

// SendImage 实现 tools.ImageSender：先上传换 image_key，再用 image 消息回复。
// 飞书不能直接发本地文件，必须先走 /im/v1/images 上传。
func (r *FeishuReporter) SendImage(ctx context.Context, absPath string) error {
	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("打开图片失败：%w", err)
	}
	defer f.Close()

	resp, err := r.client.Im.Image.Create(ctx, larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().
			ImageType(larkim.CreateImageImageTypeMessage).
			Image(f).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("上传图片失败：%w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("上传图片被拒绝：code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.ImageKey == nil {
		return fmt.Errorf("上传成功但没有返回 image_key")
	}

	content, err := json.Marshal(map[string]string{"image_key": *resp.Data.ImageKey})
	if err != nil {
		return fmt.Errorf("序列化图片消息失败：%w", err)
	}

	body := larkim.NewReplyMessageReqBodyBuilder().
		MsgType("image").
		Content(string(content)).
		Build()
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(r.messageID).
		Body(body).
		Build()

	if _, err := r.client.Im.Message.Reply(ctx, req); err != nil {
		return fmt.Errorf("发送图片消息失败：%w", err)
	}
	return nil
}
