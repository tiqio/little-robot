package feishu

import (
	"context"
	"encoding/json"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"little-robot/internal/engine"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Bot struct {
	appID     string
	appSecret string
	eng       *engine.AgentEngine
	client    *lark.Client

	mu    sync.Mutex
	seen  map[string]struct{}
	locks map[string]*sync.Mutex
}

func NewBot(appID, appSecret string, eng *engine.AgentEngine) *Bot {
	return &Bot{
		appID:     appID,
		appSecret: appSecret,
		eng:       eng,
		client:    lark.NewClient(appID, appSecret),
		seen:      make(map[string]struct{}),
		locks:     make(map[string]*sync.Mutex),
	}
}

func (b *Bot) Start(ctx context.Context) error {
	handler := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(b.onMessage)
	cli := larkws.NewClient(b.appID, b.appSecret, larkws.WithEventHandler(handler))
	log.Println("[Feishu] 长连接启动中...")
	return cli.Start(ctx)
}

func (b *Bot) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}
	msg := event.Event.Message

	messageID := deref(msg.MessageId)
	chatID := deref(msg.ChatId)

	// 去重：飞书没有收到响应会重推，不去重模型会把同一件事做好几遍
	if !b.markSeen(messageID) {
		log.Printf("[Feishu] 重复事件，忽略：%s", messageID)
		return nil
	}

	if deref(msg.MessageType) != "text" {
		return nil
	}

	text := extractText(deref(msg.Content))
	if text == "" {
		return nil
	}
	log.Printf("[Feishu] 收到消息 chat=%s：%s", chatID, text)

	reporter := NewFeishuReporter(b.client, messageID)

	// 同一个绘画串行，避免两个任务同时修改同一个工作区
	lock := b.lockFor(chatID)
	if !lock.TryLock() {
		go reporter.NotifyBusy(context.Background())
		return nil
	}

	// 立刻返回，任务放后台；用Background，不继承handler的ctx
	go func() {
		defer lock.Unlock()
		runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		if err := b.eng.Run(runCtx, text, reporter); err != nil {
			log.Printf("[Feishu] 引擎出错：%v", err)
			reporter.NotifyError(context.Background(), err)
		}
	}()

	return nil
}

func (b *Bot) markSeen(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.seen[id]; ok {
		return false
	}
	b.seen[id] = struct{}{}
	if len(b.seen) > 1000 {
		b.seen = make(map[string]struct{})
	}
	return true
}

func (b *Bot) lockFor(chatID string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	if l, ok := b.locks[chatID]; ok {
		return l
	}
	l := &sync.Mutex{}
	b.locks[chatID] = l
	return l
}

var mentionRe = regexp.MustCompile(`@_user_\d+`)

// 群里@机器人会变成@_user_1这样的占位符，要剥掉
func extractText(content string) string {
	var c struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &c); err != nil {
		return ""
	}
	return strings.TrimSpace(mentionRe.ReplaceAllString(c.Text, ""))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
