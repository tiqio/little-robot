package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSender 同时实现 ImageSender + FileSender：
// 记录各自通道收到了什么，验证 send 的自动路由
type fakeSender struct {
	imagePath string
	filePath  string
	fileName  string
	fileType  string
}

func (f *fakeSender) SendImage(ctx context.Context, absPath string) error {
	f.imagePath = absPath
	return nil
}

func (f *fakeSender) SendFile(ctx context.Context, absPath, fileName, fileType string) error {
	f.filePath, f.fileName, f.fileType = absPath, fileName, fileType
	return nil
}

func sendCtx(t *testing.T, s *fakeSender) context.Context {
	t.Helper()
	return WithFileSender(WithImageSender(context.Background(), s), s)
}

func writeAt(t *testing.T, dir, rel string, size int) string {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, size)
	copy(data, []byte("fake bytes"))
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

// 自动路由表：图片走 image 通道，其余走 file 通道
func TestSendRouting(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "out/photo.jpg", 1<<20)
	writeAt(t, dir, "out/report.pdf", 512)
	writeAt(t, dir, "out/book.zip", 512)
	// 12MB 的 png：超过图片通道 10MB 上限，降级走 file
	writeAt(t, dir, "out/big.png", 12<<20)

	cases := []struct {
		name     string
		args     string
		wantMode string // "image" 或 "file"
		wantType string // file 通道的 fileType
		wantName string
	}{
		{"小图走图片通道", `{"path":"out/photo.jpg"}`, "image", "", "photo.jpg"},
		{"pdf 走文件通道", `{"path":"out/report.pdf"}`, "file", "pdf", "report.pdf"},
		{"zip 走 stream", `{"path":"out/book.zip"}`, "file", "stream", "book.zip"},
		{"超大图降级文件通道", `{"path":"out/big.png"}`, "file", "stream", "big.png"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &fakeSender{}
			out, err := NewSendTool(dir).Execute(sendCtx(t, s), json.RawMessage(c.args))
			if err != nil {
				t.Fatalf("执行失败：%v（%s）", err, out)
			}
			switch c.wantMode {
			case "image":
				if s.imagePath == "" || filepath.Base(s.imagePath) != "photo.jpg" {
					t.Errorf("应该走 image 通道，实际 imagePath=%q filePath=%q", s.imagePath, s.filePath)
				}
			case "file":
				if s.filePath == "" || filepath.Base(s.filePath) != c.wantName {
					t.Errorf("应该走 file 通道（%s），实际 filePath=%q", c.wantName, s.filePath)
				}
				if s.fileType != c.wantType {
					t.Errorf("fileType=%s，期望 %s", s.fileType, c.wantType)
				}
			}
		})
	}
}

// name 参数：文件通道可用自定义文件名；图片通道飞书没有文件名概念
func TestSendCustomName(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "out/report.pdf", 512)
	s := &fakeSender{}
	_, err := NewSendTool(dir).Execute(sendCtx(t, s),
		json.RawMessage(`{"path":"out/report.pdf","name":"扫描版.pdf"}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.fileName != "扫描版.pdf" {
		t.Errorf("fileName=%s，期望 扫描版.pdf", s.fileName)
	}
}

// 沙箱契约：无论模型传什么路径，sender 看到的文件一定在工作区内
func TestSendStaysInSandbox(t *testing.T) {
	dir := t.TempDir()

	// 1) 带 ../ 但解析后仍在内部
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAt(t, dir, "a/b/real.pdf", 1)
	s := &fakeSender{}
	if _, err := NewSendTool(dir).Execute(sendCtx(t, s),
		json.RawMessage(`{"path":"a/b/../b/real.pdf"}`)); err != nil {
		t.Fatalf("内部 ../ 应该成功：%v", err)
	}
	if !strings.HasPrefix(s.filePath, dir+string(os.PathSeparator)) {
		t.Errorf("路径逃出了工作区：%s", s.filePath)
	}
	if filepath.Base(s.filePath) != "real.pdf" {
		t.Errorf("解析到了错误的文件：%s", s.filePath)
	}

	// 2) 真的越界（../ 走到工作区外）：要么被沙箱拦下，要么文件不存在——总之发不出去
	out, err := NewSendTool(dir).Execute(sendCtx(t, &fakeSender{}),
		json.RawMessage(`{"path":"../../../../etc/hostname.pdf"}`))
	if err == nil {
		t.Errorf("越界路径应该失败，却成功了：%s", out)
	}
}

// 空文件被飞书拒绝，提前在工具层拦住
func TestSendRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "out/empty.pdf", 0)
	_, err := NewSendTool(dir).Execute(sendCtx(t, &fakeSender{}),
		json.RawMessage(`{"path":"out/empty.pdf"}`))
	if err == nil || !strings.Contains(err.Error(), "空文件") {
		t.Errorf("空文件应该报'空文件'，实际：%v", err)
	}
}

// 30MB 是文件通道的硬上限
func TestSendRejectsTooBig(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "out/huge.bin", 31<<20)
	_, err := NewSendTool(dir).Execute(sendCtx(t, &fakeSender{}),
		json.RawMessage(`{"path":"out/huge.bin"}`))
	if err == nil || !strings.Contains(err.Error(), "30MB") {
		t.Errorf("31MB 应该报 30MB 上限，实际：%v", err)
	}
}

// 环境没有对应 sender 时报错要说到点子上（比如终端外壳只有 image 没有 file）
func TestSendMissingSender(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "out/photo.jpg", 1024)
	writeAt(t, dir, "out/report.pdf", 1024)

	// 只有 image sender，发 pdf 要报"不支持发送文件"
	onlyImage := &fakeSender{}
	_, err := NewSendTool(dir).Execute(WithImageSender(context.Background(), onlyImage),
		json.RawMessage(`{"path":"out/report.pdf"}`))
	if err == nil || !strings.Contains(err.Error(), "发送文件") {
		t.Errorf("缺 FileSender 应该报'发送文件'，实际：%v", err)
	}

	// 只有 file sender，发图也要能用（file 通道兜底）
	onlyFile := &fakeSender{}
	if _, err := NewSendTool(dir).Execute(WithFileSender(context.Background(), onlyFile),
		json.RawMessage(`{"path":"out/photo.jpg"}`)); err != nil {
		t.Errorf("只有 FileSender 时图片应该走 file 通道兜底：%v", err)
	}

	// 什么都没有
	if _, err := NewSendTool(dir).Execute(context.Background(),
		json.RawMessage(`{"path":"out/photo.jpg"}`)); err == nil {
		t.Error("两个 sender 都没有应该报错")
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

// 路由边界：isImageExt 认哪些后缀
func TestIsImageExt(t *testing.T) {
	cases := map[string]bool{
		"a.jpg": true, "a.jpeg": true, "a.png": true, "a.webp": true, "a.gif": true,
		"A.JPEG": true,
		"a.pdf":  false, "a.zip": false, "a.svg": false, "a": false,
	}
	for name, want := range cases {
		if got := isImageExt("/x/" + name); got != want {
			t.Errorf("isImageExt(%s) = %v，期望 %v", name, got, want)
		}
	}
}

// 展示名清洗：线上真实事故
//
//	name="魔法少女伊莉雅…（PART.1）1/2"
//	→ 没有 .pdf 手机认不出；斜杠被客户端当路径切开，用户只看到 "2"
func TestSanitizeFileName(t *testing.T) {
	pdfPath := "/w/out/hentai/681824/681824-part1.pdf"
	cases := []struct {
		name string
		in   string
		want string
	}{
		// 没后缀 → 补真实后缀
		{"缺后缀", "魔法少女伊莉雅 本子", "魔法少女伊莉雅 本子.pdf"},
		// 斜杠 → 短横线（不能再被当路径切开）
		{"带斜杠", "伊莉雅本子（PART.1）1/2", "伊莉雅本子（PART.1）1-2.pdf"},
		{"书名号", "《伊莉雅》1/2", "伊莉雅 1-2.pdf"},
		// 后缀已经对：原样
		{"已有pdf", "伊莉雅（13页）.pdf", "伊莉雅（13页）.pdf"},
		{"大写PDF", "伊莉雅.PDF", "伊莉雅.PDF"},
		// 假后缀：以真实文件为准
		{"错后缀", "伊莉雅.zip", "伊莉雅.pdf"},
		// 空名字 → 用磁盘原名
		{"空名字", "   ", "681824-part1.pdf"},
		// 全非法字符 → 兜底磁盘名
		{"全非法", "///", "681824-part1.pdf"},
	}
	for _, c := range cases {
		if got := sanitizeFileName(c.in, pdfPath); got != c.want {
			t.Errorf("%s：sanitizeFileName(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// 超长名字：截断但必须保住扩展名
func TestSanitizeFileNameTooLong(t *testing.T) {
	long := strings.Repeat("很长", 80)
	got := sanitizeFileName(long, "/w/a.pdf")
	if !strings.HasSuffix(got, ".pdf") {
		t.Errorf("截断后必须保住 .pdf：%q", got)
	}
	if n := len([]rune(got)); n > maxFileNameRunes {
		t.Errorf("名字太长：%d 个字符", n)
	}
}

// 端到端：事故里的 name 经过 send 之后，交给飞书的文件名必须带后缀、不含斜杠
func TestSendReportsUsableFileName(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "out/hentai/681824"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "out/hentai/681824/681824-part1.pdf")
	if err := os.WriteFile(p, []byte("%PDF-1.4 fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := &fakeSender{}
	ctx := WithFileSender(context.Background(), fs)
	tool := NewSendTool(dir)

	_, err := tool.Execute(ctx, json.RawMessage(`{"path":"out/hentai/681824/681824-part1.pdf","name":"魔法少女伊莉雅 本子（IriKuro log PART.1）1/2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if fs.fileName == "" {
		t.Fatal("没有走到文件通道")
	}
	if strings.ContainsAny(fs.fileName, `/\`) {
		t.Errorf("文件名不该含路径分隔符：%q", fs.fileName)
	}
	if !strings.HasSuffix(fs.fileName, ".pdf") {
		t.Errorf("文件名必须以 .pdf 结尾（否则手机认不出）：%q", fs.fileName)
	}
	if fs.fileType != "pdf" {
		t.Errorf("file_type 应该是 pdf，实际 %q", fs.fileType)
	}
	t.Logf("发给飞书的文件名：%s", fs.fileName)
}

// 过短的名字是模型写残了（"2"），要用磁盘原名兜底
func TestSanitizeFileNameTooShort(t *testing.T) {
	got := sanitizeFileName("2", "/w/out/hentai/681824/681824-part2.pdf")
	if got != "681824-part2.pdf" {
		t.Errorf("过短名字应该兜底成磁盘名，实际 %q", got)
	}
}
