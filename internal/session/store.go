package session

import (
	"encoding/json"
	"fmt"
	"little-robot/internal/schema"
	"os"
	"path/filepath"
	"strings"
	"time"
)

/*
	json.Marshal(session)会有问题：
		1. history是小写，encoding/json只序列化导出字段 ——> 存出来的JSON里没有对话，等于没有存
		2. mu会被序列化（如果它到处导出），而且下次加载时反序列化出来的mutex是零值——看起来能用但是不是任务的那个状态
	Go 的规则是：首字母大写 = 导出（public），小写 = 不导出（private）。encoding/json 是靠反射读字段的，而反射有一条硬限制：不能读取或写入其他包的未导出字段。
	所以标准做法是影子结构：落盘用一套字段，内存用另一套，中间手工搬一次，格式和结构解耦，以后内存结构怎么改，只要影子结构和搬运动作跟着改，磁盘上的老文件还能读
*/

// fileFormat 是落盘格式，和内存结构解耦
// 内存里有锁，有unexported字段，直接序列化既写不出去也不稳定
type fileFormat struct {
	ID        string           `json:"id"`
	WorkDir   string           `json:"workdir"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	History   []schema.Message `json:"history"`
}

// safeName 把会话id变成安全的文件名，防止id里出现/或..
func safeName(id string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", "..", "_", " ", "_") // 统一把特殊字符转换成_
	return r.Replace(id)
}

// Save 把会话写到dir/<id>.json
func (s *Session) Save(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建会话目录失败：%w", err)
	}

	// 快照：读的时候要持锁，拷完立刻放锁，别把几百毫秒的序列化留在锁里
	s.mu.RLock()
	hist := make([]schema.Message, len(s.history))
	copy(hist, s.history)
	s.mu.RUnlock()

	// 落盘前剥掉图片：data URL 是 base64，一张 300KB 的图能让会话文件涨到 400KB；
	// 更麻烦的是——下次加载后它会被重新发给模型，每轮都按图片计费。
	// 保留一句文字说明，让模型知道"当时看过图"，而不是完全断片。
	for i := range hist {
		if len(hist[i].Images) == 0 {
			continue
		}
		n := len(hist[i].Images)
		hist[i].Images = nil
		note := ImageNote(n) // 和 compactor 同一句话，模型在两个地方看到的占位一致
		if strings.TrimSpace(hist[i].Content) == "" {
			hist[i].Content = note
		} else {
			hist[i].Content += "\n" + note
		}
	}

	snap := fileFormat{
		ID:        s.ID,
		WorkDir:   s.WorkDir,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
		History:   hist,
	}

	b, err := json.MarshalIndent(snap, "", " ")
	if err != nil {
		return "", fmt.Errorf("序列化会话失败：%w", err)
	}

	path := filepath.Join(dir, safeName(s.ID)+".json")

	// 先写临时文件再rename:rename再同一个文件系统中是原子的，中途崩掉只会留下.tmp，不会把好文件写坏
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", fmt.Errorf("写会话失败：%w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("替换会话文件失败：%w", err)
	}
	return path, nil
}

// Load 从文件恢复会话：文件不存在的时候返回os.ErrNotExist
func Load(path string) (*Session, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var f fileFormat
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("解析会话文件失败：%w", err)
	}

	return &Session{
		ID:        f.ID,
		WorkDir:   f.WorkDir,
		CreatedAt: f.CreatedAt,
		UpdatedAt: f.UpdatedAt,
		history:   f.History,
	}, nil
}
