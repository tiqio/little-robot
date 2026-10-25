package feishu

import (
	"context"
	"encoding/json"
	"little-robot/internal/engine"
	"little-robot/internal/runstate"
	"little-robot/internal/session"
	"little-robot/internal/tools"
	"little-robot/internal/worktree"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type Bot struct {
	appID     string
	appSecret string
	eng       *engine.AgentEngine
	client    *lark.Client
	sessions  *session.Manager

	mu          sync.Mutex
	seen        map[string]struct{}
	locks       map[string]*sync.Mutex
	worktreeMgr *worktree.Manager
	chatDirs    *chatDirs // 新增：chat_id -> 目录名
	chatNames   sync.Map  // 新增：进程内缓存群名，避免每轮调用API
}

// internal/worktree/worktree.go 里加一个判断，或者放在 bot 里
// 表示这个目录本身是不是一个git仓库的根
// RepoRoot(dir)问：dir所在的仓库，根在哪里？（可能是父目录）
// isRepoRoot(dir)问：dir自己就是那个根吗？
/*
	$ cd /Users/dd/AITEST/little-robot/workspace
	$ git rev-parse --show-toplevel
	/Users/dd/AITEST/little-robot          ← 根在上一层

	因为git worktree add是相对于仓库根来操作的，root == abs的比较：macOS上git返回的是解析过软连接的路径：
	git rev-parse --show-toplevel  →  /private/tmp/symtest
	filepath.Abs("/tmp/symtest")   →  /tmp/symtest          ← 不相等！

	需要用os.SameFile来比较设备号+inode，此时/tmp/x和/private/tmp/x相同
*/
func isRepoRoot(dir string) bool {
	root, err := worktree.RepoRoot(dir)
	if err != nil {
		return false
	}
	//abs, err := filepath.Abs(dir)
	//if err != nil {
	//	return false
	//}
	//return root == abs // 必须是根本身，不能是子目录

	rootInfo, err := os.Stat(root)
	if err != nil {
		return false
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return false
	}

	// 用inode比较，同时解决软链（/tmp -> /private/tmp）和大小写不敏感文件系统
	return os.SameFile(rootInfo, dirInfo)
}

func NewBot(appID, appSecret string, eng *engine.AgentEngine) *Bot {
	b := &Bot{
		appID:     appID,
		appSecret: appSecret,
		eng:       eng,
		client:    lark.NewClient(appID, appSecret),
		sessions:  session.NewManager(), // 默认不落盘，兼容旧行为
		seen:      make(map[string]struct{}),
		locks:     make(map[string]*sync.Mutex),
	}

	b.chatDirs = newChatDirs(eng.WorkDir)

	if isRepoRoot(eng.WorkDir) {
		if mgr, err := worktree.NewManager(eng.WorkDir); err == nil {
			b.worktreeMgr = mgr
		} else {
			log.Printf("[Feishu] worktree初始化失败，改用普通子目录：%v", err)
		}
	} else {
		log.Printf("[Feishu] 工作区%s不是git仓库根，不启用worktree（用普通子目录隔离）", eng.WorkDir)
	}
	return b
}

// UseSessions 允许调用方注入持久化管理器（飞书多轮会话需要跨进程记忆）
// 为什么不改NewBot的签名？一改就破坏所有调用方（虽然现在只有cmd/bot），加一个可选的setter是兼容性成本最低的做法，等确定永远需要持久化，再把参数挪近构造函数
func (b *Bot) UseSessions(m *session.Manager) {
	if m != nil {
		b.sessions = m
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

		// 把本次运行的 sender 放进 ctx：send_image / send_file 工具靠它知道"回复哪条消息"
		runCtx = tools.WithImageSender(runCtx, reporter)
		runCtx = tools.WithFileSender(runCtx, reporter)

		// 每个群一个独立目录（也解决了之前“共用一个workspace”的问题）
		//if b.worktreeMgr != nil {
		// 无条件注入：让workDirFor自己决定用worktree还是子目录
		// 每次运行一个工作目录；历史也跟着这个目录走
		workDir := b.workDirFor(runCtx, chatID) // 1. 先定目标
		sessDir := filepath.Join(workDir, ".claw", "sessions")

		rs := runstate.New()
		rs.WorkDir = workDir
		rs.ChatID = chatID
		rs.ChatName = filepath.Base(workDir) // 2. 顺手把群名称带上
		runCtx = runstate.With(runCtx, rs)
		//}

		// 最小修法，bot里面建立一个共享会话（更好的做法是按照chatID分会话，每个用户共享记忆，但是这里先让它跑通）
		// 现在改成了chatID，每个群一份记忆，写"feishu-default"会让A群的问题污染B群的答案"
		sess := b.sessions.GetOrCreateAt(chatID, b.eng.WorkDir, sessDir)
		if err := b.eng.Run(runCtx, sess, text, reporter); err != nil {
			log.Printf("[Feishu] 引擎出错：%v", err)
			reporter.NotifyError(context.Background(), err)
		}

		// 跑完就落盘：不存的话重启就等于全员失忆
		if path, err := b.sessions.Save(sess); err != nil { // 3. 跑完就存
			log.Printf("[Feishu] 保存会话失败：%v", err)
		} else if path != "" {
			log.Printf("[Feishu] 会话已保存：%s", path)
		}
	}()

	return nil
}

/*
dirFor 未命中（chats.json 里没这个群）
→ 闭包执行：可以防止每条消息都会打一次飞书API，写成闭包后，“算名字”这件事只在“真的需要”时发生——惰性求值

	→ b.chatName()
	     → 先查 chatNames（内存缓存，避免连续两次 API）
	     → 没有再调 API
*/
func (b *Bot) workDirFor(ctx context.Context, chatID string) string {
	// 目录名只在首次出现时算一次，结果固化在 .claw/chats.json 里
	name := b.chatDirs.dirFor(chatID, func() string {
		return b.chatName(ctx, chatID) // 拿不到就返回 ""，退化成纯 shortID
	})

	if b.worktreeMgr != nil {
		path, created, err := b.worktreeMgr.Ensure("chat-" + name)
		if err == nil {
			if created {
				log.Printf("[Feishu] 为会话 %s 新建 worktree：%s", name, path)
			}
			return path
		}
		log.Printf("[Feishu] worktree 不可用，改用子目录：%v", err)
	}

	dir := filepath.Join(b.eng.WorkDir, "chats", name)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[Feishu] 建会话目录失败：%v", err)
	}
	return dir
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
