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
	"strings"
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
	// galleries 已经"拉过/发过"的本子 id：内容 hash 挡不住"同一本重新生成 PDF"
	// （两次生成的文件字节不同），所以还需要 id 这一层的记忆。
	galleries map[string]time.Time
	// sources 已经下载过的图片来源（URL 哈希）：同一张图被 CDN 重新编码后字节会变，
	// 内容 hash 对不上，但"来源还是那个 URL"——这一层防的是"又是同一张图"。
	sources map[string]sourceEntry
	// perceptual 图片感知哈希：字节变了但画面一样（换 CDN / 重新压缩）也能认出来。
	// 用路径链表的场景：模型把同一张图下载成 15.jpg、17.jpg 两个文件，字节不同。
	perceptual map[string]time.Time
}

// sourceEntry 一次图片下载记录（路径用于"直接复用已有文件"）
type sourceEntry struct {
	Path string    `json:"path"`
	At   time.Time `json:"at"`
}

// ledgerFile 落盘格式（v2）：老的纯 map 格式也能读
type ledgerFile struct {
	Items      map[string]sentEntry   `json:"items"`
	Galleries  map[string]time.Time   `json:"galleries,omitempty"`
	Sources    map[string]sourceEntry `json:"sources,omitempty"`
	Perceptual map[string]time.Time   `json:"perceptual,omitempty"`
}

// 同一个路径共用一个实例：send 和 fetch_hentai 都要读写这张表，
// 各自 New 一个会互相覆盖（并发写同一个 json）。
var (
	ledgerMu     sync.Mutex
	ledgerByPath = map[string]*SentLedger{}
)

// sharedLedger 按路径取单例
func sharedLedger(path string) *SentLedger {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	if l, ok := ledgerByPath[path]; ok {
		return l
	}
	l := NewSentLedger(path)
	ledgerByPath[path] = l
	return l
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
	l := &SentLedger{
		path:       path,
		items:      make(map[string]sentEntry),
		galleries:  make(map[string]time.Time),
		sources:    make(map[string]sourceEntry),
		perceptual: make(map[string]time.Time),
	}
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
	// v2 格式
	var lf ledgerFile
	if err := json.Unmarshal(data, &lf); err == nil && (lf.Items != nil || lf.Galleries != nil || lf.Sources != nil) {
		if lf.Items != nil {
			l.items = lf.Items
		}
		if lf.Galleries != nil {
			l.galleries = lf.Galleries
		}
		if lf.Sources != nil {
			l.sources = lf.Sources
		}
		if lf.Perceptual != nil {
			l.perceptual = lf.Perceptual
		}
		return
	}
	// 兼容 v1：整个文件就是一个 hash -> entry 的 map
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
	data, err := json.MarshalIndent(ledgerFile{
		Items:      l.items,
		Galleries:  l.galleries,
		Sources:    l.sources,
		Perceptual: l.perceptual,
	}, "", " ")
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

// SeenGallery 这个本子是否已经拉过（返回最后一次时间）
func (l *SentLedger) SeenGallery(id string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	at, ok := l.galleries[id]
	return at, ok
}

// MarkGallery 记下"这个本子拉过了"，立刻落盘
func (l *SentLedger) MarkGallery(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.galleries[id] = time.Now()
	if len(l.galleries) > 500 {
		// 只留最近 500 条，避免无限增长
		cut := time.Now().AddDate(0, 0, -30)
		for k, v := range l.galleries {
			if v.Before(cut) {
				delete(l.galleries, k)
			}
		}
	}
	return l.saveLocked()
}

// sourceKey URL → 短哈希（URL 太长，表里不存原文）
func sourceKey(rawURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(rawURL)))
	return hex.EncodeToString(sum[:])[:16]
}

// SeenSource 这个来源是否下载过；返回上次落盘的路径，便于直接复用
func (l *SentLedger) SeenSource(rawURL string) (string, time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.sources[sourceKey(rawURL)]
	return e.Path, e.At, ok
}

// MarkSource 记一次下载（立刻落盘）
func (l *SentLedger) MarkSource(rawURL, relPath string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sources[sourceKey(rawURL)] = sourceEntry{Path: relPath, At: time.Now()}
	if len(l.sources) > 2000 {
		cut := time.Now().AddDate(0, 0, -30)
		for k, v := range l.sources {
			if v.At.Before(cut) {
				delete(l.sources, k)
			}
		}
	}
	return l.saveLocked()
}

// SeenPerceptual 画面是否已经发过（同一张图换编码也认得出）
func (l *SentLedger) SeenPerceptual(hash string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	at, ok := l.perceptual[hash]
	return at, ok
}

// MarkPerceptual 记一条感知哈希（立刻落盘）
func (l *SentLedger) MarkPerceptual(hash string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.perceptual[hash] = time.Now()
	if len(l.perceptual) > 2000 {
		cut := time.Now().AddDate(0, 0, -60)
		for k, v := range l.perceptual {
			if v.Before(cut) {
				delete(l.perceptual, k)
			}
		}
	}
	return l.saveLocked()
}
