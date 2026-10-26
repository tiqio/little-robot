package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SentLedger 记录"已经发给用户的内容"（按内容哈希），防止同一个文件被重复发送。
//
// 为什么需要它：模型经常连着调 send 发同一个封面/PDF（尤其在一轮里试探性重试），
// 用户看到的就是"说抓几个，结果传的都是同一张"。光靠提示词约束不住。
//
// 为什么落盘：不落盘的话，重启后模型会把 out/ 里已有的东西再发一遍。
// 现在每次 send 成功都立刻写盘，启动时再扫一遍 out/ 把已知内容登记进去。
type SentLedger struct {
	mu    sync.Mutex
	path  string
	items map[string]sentEntry
}

// sentEntry 一条发送记录
type sentEntry struct {
	Path string    `json:"path"`           // 工作区相对路径（最后一次）
	Name string    `json:"name,omitempty"` // 当时的展示名
	Size int64     `json:"size"`
	At   time.Time `json:"at"`
	// Sent 是否真的发出去过。启动时扫 out/ 只登记为 false（只证明"存在过"），
	// 不会因此拦住用户主动要求发送。
	Sent bool `json:"sent"`
}

// NewSentLedger 打开/创建记录表；path 为空表示只在内存里记（测试用）
func NewSentLedger(path string) *SentLedger {
	l := &SentLedger{path: path, items: make(map[string]sentEntry)}
	if path != "" {
		l.load()
		// 启动时把 out/ 里已存在的内容登记进来：
		// 这些文件可能是上个进程发过的，重启后不该再发一遍
		l.warmFromOut(filepath.Join(filepath.Dir(path), ".."))
	}
	return l
}

// load 读盘（文件不存在/损坏都当空表，不能让发送功能因此不可用）
func (l *SentLedger) load() {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	var items map[string]sentEntry
	if err := json.Unmarshal(data, &items); err != nil {
		log.Printf("[Ledger] 记录表损坏，按空表处理：%v", err)
		return
	}
	if items != nil {
		l.items = items
	}
}

// saveLocked 写盘（调用方已持锁）
func (l *SentLedger) saveLocked() error {
	if l.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("创建记录表目录失败：%w", err)
	}
	data, err := json.MarshalIndent(l.items, "", " ")
	if err != nil {
		return fmt.Errorf("序列化记录表失败：%w", err)
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写记录表失败：%w", err)
	}
	return os.Rename(tmp, l.path)
}

// Seen 这个内容是否已经发过
func (l *SentLedger) Seen(hash string) (sentEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.items[hash]
	return e, ok && e.Sent
}

// Record 记一条"已发送"并立刻落盘
func (l *SentLedger) Record(hash string, e sentEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.Sent = true
	if e.At.IsZero() {
		e.At = time.Now()
	}
	l.items[hash] = e
	if len(l.items) > 2000 {
		l.pruneLocked()
	}
	return l.saveLocked()
}

// pruneLocked 表太大时清掉"只登记没发送"的条目（发送记录全留）
func (l *SentLedger) pruneLocked() {
	for k, v := range l.items {
		if !v.Sent {
			delete(l.items, k)
		}
	}
}

// warmFromOut 扫工作区的 out/ 目录，把已有文件的哈希登记进来（Sent=false）。
// 这样重启后同一份内容不会被当成"新东西"再发；但也不会阻止用户主动要发。
func (l *SentLedger) warmFromOut(workDir string) int {
	root := filepath.Join(workDir, "out")
	var added int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() == 0 {
			return nil
		}
		hash, herr := fileHash(path)
		if herr != nil {
			return nil
		}
		l.mu.Lock()
		if _, ok := l.items[hash]; !ok {
			rel, _ := filepath.Rel(workDir, path)
			l.items[hash] = sentEntry{Path: rel, Size: info.Size(), At: info.ModTime(), Sent: false}
			added++
		}
		l.mu.Unlock()
		return nil
	})
	if err != nil {
		return added
	}
	if added > 0 {
		log.Printf("[Ledger] 从 out/ 登记了 %d 个已有文件（重启后不会重复发送）", added)
		l.mu.Lock()
		_ = l.saveLocked()
		l.mu.Unlock()
	}
	return added
}

// fileHash 内容哈希（取前 16 字节十六进制，够防重）
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
