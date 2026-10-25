package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFileSender：记录调用，验证工具侧的路径沙箱 + fileType 推断
type fakeFileSender struct {
	path     string
	fileName string
	fileType string
}

func (f *fakeFileSender) SendImage(ctx context.Context, absPath string) error { return nil }
func (f *fakeFileSender) SendFile(ctx context.Context, absPath, fileName, fileType string) error {
	f.path, f.fileName, f.fileType = absPath, fileName, fileType
	return nil
}

func TestSendFileInfersType(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "out")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "report.pdf"), []byte("%PDF-1.4 fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "book.zip"), []byte("PK fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		args     string
		wantType string
		wantName string
		wantBase string
	}{
		{`{"path":"out/report.pdf"}`, "pdf", "report.pdf", "report.pdf"},
		{`{"path":"out/book.zip"}`, "stream", "book.zip", "book.zip"},
		{`{"path":"out/report.pdf","name":"扫描版.pdf"}`, "pdf", "扫描版.pdf", "report.pdf"},
	}
	for _, c := range cases {
		sender := &fakeFileSender{}
		ctx := WithFileSender(context.Background(), sender)
		tool := NewSendFileTool(dir)

		if out, err := tool.Execute(ctx, json.RawMessage(c.args)); err != nil {
			t.Fatalf("args=%s 执行失败：%v（%s）", c.args, err, out)
		}
		if sender.fileType != c.wantType {
			t.Errorf("args=%s fileType=%s，期望 %s", c.args, sender.fileType, c.wantType)
		}
		if sender.fileName != c.wantName {
			t.Errorf("args=%s fileName=%s，期望 %s", c.args, sender.fileName, c.wantName)
		}
		if filepath.Base(sender.path) != c.wantBase {
			t.Errorf("args=%s 实际文件=%s，期望 %s", c.args, sender.path, c.wantBase)
		}
	}
}

// 沙箱契约：无论模型传什么路径，sender 看到的文件一定在工作区内
func TestSendFileStaysInSandbox(t *testing.T) {
	dir := t.TempDir()

	// 1) 带 ../ 但解析后仍在内部
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "b", "real.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sender := &fakeFileSender{}
	tool := NewSendFileTool(dir)
	if _, err := tool.Execute(WithFileSender(context.Background(), sender),
		json.RawMessage(`{"path":"a/b/../b/real.pdf"}`)); err != nil {
		t.Fatalf("内部 ../ 应该成功：%v", err)
	}
	if !strings.HasPrefix(sender.path, dir+string(os.PathSeparator)) {
		t.Errorf("路径逃出了工作区：%s", sender.path)
	}
	if filepath.Base(sender.path) != "real.pdf" {
		t.Errorf("解析到了错误的文件：%s", sender.path)
	}

	// 2) 真的越界（../ 走到工作区外）：要么被沙箱拦下，要么文件不存在——总之发不出去
	tool2 := NewSendFileTool(dir)
	out, err := tool2.Execute(WithFileSender(context.Background(), &fakeFileSender{}),
		json.RawMessage(`{"path":"../../../../etc/hostname.pdf"}`))
	if err == nil {
		t.Errorf("越界路径应该失败，却成功了：%s", out)
	}
}

// 空文件被飞书拒绝，提前在工具层拦住
func TestSendFileRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "empty.pdf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewSendFileTool(dir).Execute(WithFileSender(context.Background(), &fakeFileSender{}),
		json.RawMessage(`{"path":"out/empty.pdf"}`))
	if err == nil || !strings.Contains(err.Error(), "空文件") {
		t.Errorf("空文件应该报'空文件'，实际：%v", err)
	}
}

// fileType 推断表
func TestInferFileType(t *testing.T) {
	cases := map[string]string{
		"a.pdf":  "pdf",
		"a.mp4":  "mp4",
		"a.docx": "doc",
		"a.xlsx": "xls",
		"a.pptx": "ppt",
		"a.opus": "opus",
		"a.zip":  "stream",
		"a.epub": "stream",
		"A.PDF":  "pdf", // 大小写不敏感
	}
	for name, want := range cases {
		if got := inferFileType("/x/" + name); got != want {
			t.Errorf("inferFileType(%s) = %s，期望 %s", name, got, want)
		}
	}
}
