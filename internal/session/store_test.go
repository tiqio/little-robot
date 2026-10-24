package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"little-robot/internal/schema"
)

// 落盘时必须剥掉图片，只留文字说明
func TestSaveStripsImages(t *testing.T) {
	dir := t.TempDir()
	s := New("t1", dir)

	// 一条纯图片消息（工具注入的形态）
	s.Append(schema.Message{
		Role:   schema.RoleUser,
		Images: []string{"data:image/jpeg;base64," + strings.Repeat("A", 5000)},
	})
	// 一条同时有文字和图片的消息
	s.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: "这是金毛的照片",
		Images:  []string{"data:image/jpeg;base64,AAAA"},
	})

	path, err := s.Save(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatalf("保存失败：%v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if strings.Contains(string(raw), "base64") {
		t.Fatalf("落盘文件里仍然有图片数据：%.200s", string(raw))
	}

	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(f.History) != 2 {
		t.Fatalf("消息数不对：%d", len(f.History))
	}
	if !strings.Contains(f.History[0].Content, "查看过 1 张图片") {
		t.Fatalf("纯图片消息没留下说明：%q", f.History[0].Content)
	}
	if !strings.Contains(f.History[1].Content, "这是金毛的照片") ||
		!strings.Contains(f.History[1].Content, "查看过 1 张图片") {
		t.Fatalf("带文字的消息处理不对：%q", f.History[1].Content)
	}
	if len(f.History[0].Images) != 0 || len(f.History[1].Images) != 0 {
		t.Fatal("Images 没有清空")
	}
}

// 内存里的会话不受影响（运行时还要用图）
func TestSaveKeepsMemoryImages(t *testing.T) {
	dir := t.TempDir()
	s := New("t2", dir)
	s.Append(schema.Message{Role: schema.RoleUser, Images: []string{"data:image/jpeg;base64,AAAA"}})

	if _, err := s.Save(filepath.Join(dir, "sessions")); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if len(s.Messages()[0].Images) != 1 {
		t.Fatal("保存不应该改动内存里的会话")
	}
}
