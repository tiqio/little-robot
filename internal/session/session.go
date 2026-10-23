package session

import (
	"little-robot/internal/schema"
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
}

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
}

// 尽量不要用包级全局遍历GlobalSessionMgr，而是在main里创建一次传下去
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
	}
}

func (m *Manager) GetOrCreate(id, workDir string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		return s
	}
	s := New(id, workDir)
	m.sessions[id] = s
	return s
}
