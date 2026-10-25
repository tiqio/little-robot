package feishu

import (
	"context"
	"encoding/json"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// 如果每次都用当前群名，群一改名 ——> 新目录、空目录 ——> 之前的文件消失（其实是留在旧目录里），所以要有一张chat_id -> 目录名的映射，首次命名后不再变

// chatDirs 记录chat_id -> 目录名，一旦确定就不再变
// 群改名不能让工作目录搬家，否则之前文件会消失
type chatDirs struct {
	mu   sync.Mutex
	path string
	m    map[string]string
}

func newChatDirs(workDir string) *chatDirs {
	// 在启动的时候会读取一次这个chats.json
	c := &chatDirs{
		path: filepath.Join(workDir, ".claw", "chats.json"),
		m:    make(map[string]string),
	}
	if raw, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(raw, &c.m)
	}
	return c
}

// dirFor拿这个名字；没有就现算一个并落盘，只有dirFor没有命中的时候才去查询chatNames
func (c *chatDirs) dirFor(chatID string, pretty func() string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if d, ok := c.m[chatID]; ok {
		return d
	}

	name := ""
	if pretty != nil {
		name = sanitizeDirName(pretty())
	}
	// 没拿到群名（权限没开/网络抖动）就先不固化：
	// 固化了就永远是 shortID，等权限补上也不会再改名了
	if name == "" {
		return shortID(chatID)
	}

	d := name + "-" + shortID(chatID) // 加短id：两个群同名也不会撞
	c.m[chatID] = d
	c.saveLocked()
	return d
}

func (c *chatDirs) saveLocked() {
	raw, err := json.MarshalIndent(c.m, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0755); err != nil {
		return
	}
	_ = os.WriteFile(c.path, raw, 0o644)
}

// chatName 调飞书拿群名；失败返回空串（调用方自己降级）
func (b *Bot) chatName(ctx context.Context, chatID string) string {
	if v, ok := b.chatNames.Load(chatID); ok {
		return v.(string)
	}

	resp, err := b.client.Im.Chat.Get(ctx, larkim.NewGetChatReqBuilder().
		ChatId(chatID).Build())
	if err != nil || !resp.Success() || resp.Data == nil || resp.Data.Name == nil {
		//b.chatNames.Store(chatID, "") 不缓存失败，下次再试
		return ""
	}

	name := *resp.Data.Name
	b.chatNames.Store(chatID, name)
	return name
}

// sanitizeDirName 只保留中文/字母/数字/-_，其余换成-并限制长度
func sanitizeDirName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r): // 中文也算 Letter
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	out := strings.Trim(b.String(), "-")
	if rs := []rune(out); len(rs) > 24 {
		out = string(rs[:24])
	}
	return out
}

// shortID 从oc_xxx里取6位做后缀，保证目录名唯一且稳定
func shortID(chatID string) string {
	id := strings.TrimPrefix(chatID, "oc_")
	if len(id) > 6 {
		id = id[:6]
	}
	if id == "" {
		return "unknown"
	}
	return id
}
