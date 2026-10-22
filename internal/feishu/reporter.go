package feishu

import (
	"context"
	"encoding/json"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"little-robot/internal/engine"
	"little-robot/internal/schema"
	"log"
)

var _ engine.Reporter = &FeishuReporter{}

// FeishuReporter 把引擎事件推回飞书，回复到出发它的那条消息上
type FeishuReporter struct {
	client    *lark.Client
	messageID string
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
