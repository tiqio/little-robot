package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

/*
run (8.7s)
├─ llm (2.9s)     命中1408 输出331
├─ tool:bash (0.02s)
├─ tool:spawn_subagent (4.1s)
│   ├─ llm (1.1s)
│   └─ tool:bash (0.01s)
└─ llm (1.6s)

这就是Span（跨度）：一次操作+开始时间+结束时间+属性+子节点，全部落盘成JSON，就是一份trace
*/

type traceKey struct{}

// Span 是一次操作的跨度，可以嵌套成树
type Span struct {
	Name       string         `json:"name"`
	StartTime  time.Time      `json:"start_time"`
	EndTime    time.Time      `json:"end_time"`
	DurationMs int64          `json:"duration_ms"`
	Attrs      map[string]any `json:"attrs,omitempty"`
	Children   []*Span        `json:"children,omitempty"`

	mu sync.Mutex // 并发工具会同时往Children里写
}

// StartSpan 开启一个子Span，并把它挂到ctx上
// 后续同一ctx链上的StartSpan会自动挂成它的孩子
func StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	sp := &Span{
		Name:      name,
		StartTime: time.Now(),
		Attrs:     map[string]any{},
	}
	if parent, ok := ctx.Value(traceKey{}).(*Span); ok && parent != nil {
		parent.mu.Lock()
		parent.Children = append(parent.Children, sp)
		parent.mu.Unlock()
	}
	return context.WithValue(ctx, traceKey{}, sp), sp
}

func (s *Span) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.EndTime.IsZero() { // 防止重复End把时长算成0，这个很重要：defer和显式调用撞在一起时会End两次，可以维持DurationMs的精准性，否则DurationMs会被算成0
		return
	}
	s.EndTime = time.Now()
	s.DurationMs = s.EndTime.Sub(s.StartTime).Milliseconds()
}

func (s *Span) Set(k string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Attrs[k] = v
}

// Save 把整棵树写成JSON，返回文件路径
func (s *Span) Save(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建trace目录失败：%w", err)
	}
	name := fmt.Sprintf("trace-%s.json", s.StartTime.Format("20060102-150405.000"))
	path := filepath.Join(dir, name)

	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return "", fmt.Errorf("序列化trace失败：%w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", fmt.Errorf("写trace失败：%w", err)
	}
	return path, nil
}
