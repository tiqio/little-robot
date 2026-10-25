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
}

func NewFeishuReporter(client *lark.Client, messageID string) *FeishuReporter {
	return &FeishuReporter{client: client, messageID: messageID}
}

func (r *FeishuReporter) OnThinkingStart(ctx context.Context) {}

func (r *FeishuReporter) OnThinkingText(ctx context.Context, text string) {}

func (r *FeishuReporter) OnAssistantMessage(ctx context.Context, text string) {
	r.send(ctx, text)
}

func (r *FeishuReporter) OnToolCall(ctx context.Context, call schema.ToolCall) {
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
		if err := r.reply(ctx, string(runes[:n])); err != nil {
			log.Printf("[Feishu] 回复失败：%v", err)
			return
		}
		runes = runes[n:]
	}
}

func (r *FeishuReporter) reply(ctx context.Context, text string) error {
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

func (r *FeishuReporter) NotifyError(ctx context.Context, err error) {
	r.send(ctx, "[执行出错 ERROR]"+err.Error())
}

func (r *FeishuReporter) NotifyBusy(ctx context.Context) {
	r.send(ctx, "上一个任务还在跑，稍后再发")
}

var _ tools.FileSender = &FeishuReporter{}

// SendFile 实现 tools.FileSender：先上传换 file_key，再用 file 消息回复。
// fileType 是飞书的枚举（pdf/mp4/doc/xls/ppt/opus/stream），由 send_file 工具推断好传进来。
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
