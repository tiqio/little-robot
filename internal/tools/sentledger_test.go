package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// 记录要落盘：重启后（新建一个 ledger）还能知道发过
func TestSentLedgerPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claw", "sent.json")

	l1 := NewSentLedger(path)
	if _, ok := l1.Seen("abc123"); ok {
		t.Fatal("空表不该说发过")
	}
	if err := l1.Record("abc123", sentEntry{Path: "out/a.jpg", Size: 10}); err != nil {
		t.Fatal(err)
	}
	if _, ok := l1.Seen("abc123"); !ok {
		t.Fatal("记录后应该认为发过")
	}

	// 模拟重启
	l2 := NewSentLedger(path)
	prev, ok := l2.Seen("abc123")
	if !ok {
		t.Fatal("重启后应该还记得发过（否则会重复发送）")
	}
	if prev.Path != "out/a.jpg" {
		t.Errorf("记录的路径不对：%q", prev.Path)
	}
}

// 启动时扫 out/ 只做"已知内容"登记，不拦用户主动发送
func TestSentLedgerWarmFromOut(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out", "hentai", "1")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(out, "cover.jpg")
	if err := os.WriteFile(img, []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := NewSentLedger(filepath.Join(dir, ".claw", "sent.json"))
	hash, err := fileHash(img)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Seen(hash); ok {
		t.Error("只登记不算发过（否则用户第一次要求发就会被拦）")
	}
	// 但表里应该有这个 hash（说明"认识这个内容"）
	l.mu.Lock()
	_, known := l.items[hash]
	l.mu.Unlock()
	if !known {
		t.Error("out/ 里已有文件应该被登记进表")
	}
}

// 内容相同、路径不同 → 同一个 hash
func TestFileHashSameContent(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jpg")
	b := filepath.Join(dir, "b.jpg")
	data := []byte("identical-content")
	if err := os.WriteFile(a, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ha, _ := fileHash(a)
	hb, _ := fileHash(b)
	if ha != hb {
		t.Errorf("同样内容应该同 hash：%s vs %s", ha, hb)
	}
}
