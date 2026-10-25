package pdf

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// makeJPEG 造一张 w×h 的图片（纯色，压缩后很小）
func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x % 255), uint8(y % 255), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 两页 PDF：结构完整、页数正确、图不拉伸
func TestBuildImagesTwoPages(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out.pdf")
	// 第 1 页竖图 600×900，第 2 页横图 900×600
	imgs := [][]byte{makeJPEG(t, 600, 900), makeJPEG(t, 900, 600)}
	if err := BuildImagesFromMemory(dst, imgs); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	if !strings.HasPrefix(s, "%PDF-1.4") {
		t.Error("缺少 PDF 头")
	}
	if !strings.HasSuffix(s, "%%EOF\n") {
		t.Error("缺少 PDF 尾")
	}
	if strings.Contains(s, "/Encrypt") {
		t.Error("PDF 不该加密（手机要能直接预览）")
	}
	if !strings.Contains(s, "/Count 2") {
		t.Errorf("页数应该是 2")
	}
	if n := strings.Count(s, "/Type /Page "); n != 2 {
		t.Errorf("Page 对象应该 2 个，实际 %d", n)
	}
	if n := strings.Count(s, "/Filter /DCTDecode"); n != 2 {
		t.Errorf("图片应该内嵌 2 个 JPEG，实际 %d", n)
	}

	// 页面高度按比例：595 * 900/600 = 892.5；595 * 600/900 = 396.67
	if !strings.Contains(s, "0 0 595.00 892.50") {
		t.Error("竖图页尺寸不对（应该保持比例）")
	}
	if !strings.Contains(s, "0 0 595.00 396.67") {
		t.Error("横图页尺寸不对（应该保持比例）")
	}

	// startxref 指向的位置必须真的是 "xref"
	re := regexp.MustCompile(`startxref\n(\d+)\n%%EOF\n$`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		t.Fatal("找不到 startxref")
	}
	var pos int
	for _, c := range m[1] {
		pos = pos*10 + int(c-'0')
	}
	if pos >= len(s) || !strings.HasPrefix(s[pos:], "xref") {
		t.Errorf("startxref=%d 指向的不是 xref 表", pos)
	}

	// xref 表里的每个偏移都要真的落在 "N 0 obj" 上
	for _, line := range strings.Split(s[pos:], "\n") {
		if !strings.HasSuffix(line, " n ") || len(line) < 18 {
			continue
		}
		var off int
		for _, c := range line[:10] {
			off = off*10 + int(c-'0')
		}
		if off >= len(s) || !regexp.MustCompile(`^\d+ 0 obj`).MatchString(s[off:]) {
			t.Errorf("xref 偏移 %d 不是对象起始位置", off)
		}
	}
}

// png 也能进 PDF（内部统一转 JPEG）
func TestBuildImagesFromPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 400))
	for x := 0; x < 300; x++ {
		for y := 0; y < 400; y++ {
			img.Set(x, y, color.RGBA{10, 200, 30, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "a.png")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.pdf")
	if err := BuildImages(dst, []string{p}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dst)
	if !strings.Contains(string(data), "/Filter /DCTDecode") {
		t.Error("png 页应该被转成内嵌 JPEG")
	}
}

// 空输入要报错，不能生成一个 0 页的坏 PDF
func TestBuildImagesEmpty(t *testing.T) {
	if err := BuildImagesFromMemory(filepath.Join(t.TempDir(), "x.pdf"), nil); err == nil {
		t.Error("空输入应该报错")
	}
}

// 坏图要报清楚是哪一页
func TestBuildImagesCorruptPage(t *testing.T) {
	err := BuildImagesFromMemory(filepath.Join(t.TempDir(), "x.pdf"), [][]byte{
		makeJPEG(t, 100, 100),
		[]byte("not an image"),
	})
	if err == nil {
		t.Fatal("坏图应该报错")
	}
	if !strings.Contains(err.Error(), "第 2 页") {
		t.Errorf("错误信息应该指出是哪一页：%v", err)
	}
}
