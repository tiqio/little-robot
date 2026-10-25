package session

import (
	"errors"
	"little-robot/internal/schema"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Session struct {
	ID        string
	WorkDir   string
	CreatedAt time.Time
	UpdatedAt time.Time

	mu      sync.RWMutex
	history []schema.Message

	// storeDir 是"这一个会话"该存到哪；空表示用 Manager 的默认目录。
	// 飞书场景每个群一个工作目录，历史也跟着群走：chats/<名字>/.claw/sessions
	storeDir string
}

// SetStoreDir 指定本会话的落盘目录；不设置就用 Manager 的默认目录
func (s *Session) SetStoreDir(dir string) { s.storeDir = dir }

func New(id, workDir string) *Session {
	now := time.Now()
	return &Session{
		ID:        id,
		WorkDir:   workDir,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, msgs...)
	s.UpdatedAt = time.Now()
}

// Messages返回历史副本，调用方随便改也污染不到会话
func (s *Session) Messages() []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]schema.Message, len(s.history)) // 把s.history复制然后copy出来
	copy(out, s.history)
	return out
}

func (s *Session) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.history)
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	dir      string // 空表示不落盘
}

// 尽量不要用包级全局遍历GlobalSessionMgr，而是在main里创建一次传下去
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
	}
}

// NewPersistentManager 创建的Manager会在GetOrCreate时尝试从dir回复会话
// 并在Save时把会话写会dir
func NewPersistentManager(dir string) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		dir:      dir,
	}
}

func (m *Manager) GetOrCreate(id, workDir string) *Session {
	return m.GetOrCreateAt(id, workDir, m.dir)
}

// GetOrCreateAt 允许为单个会话指定落盘目录（飞书：每个群一个目录）。
// 顺序：先找 storeDir，再退回 Manager 的默认目录（兼容历史文件放在默认目录的情况）。
func (m *Manager) GetOrCreateAt(id, workDir, storeDir string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[id]; ok {
		return s
	}

	for _, dir := range []string{storeDir, m.dir} {
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, safeName(id)+".json")
		if s, err := Load(path); err == nil {
			s.WorkDir = workDir // 工作区以本次启动为准，历史保留
			s.storeDir = storeDir
			m.sessions[id] = s
			return s
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[Session] 恢复会话%q失败，按新会话处理：%v", id, err)
		}
	}

	/*
		Load失败一律当新会话，不区分“文件不存在”和“文件损坏”
			1. 文件不存在 ——> 新会话，正常
			2. 文件损坏 ——> 也当新会话，但最好打一行日志，不加log的话，回遇到“会话莫名其妙丢了”，却没有任何线索
	*/
	s := New(id, workDir)
	s.storeDir = storeDir
	m.sessions[id] = s
	return s
}

// Save 把会话写回磁盘；会话自己指定了目录就优先用它，否则用 Manager 的默认目录
func (m *Manager) Save(s *Session) (string, error) {
	dir := s.storeDir
	if dir == "" {
		dir = m.dir
	}
	if dir == "" {
		return "", nil
	}
	return s.Save(dir)
}
