package tools

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// 造一张有明显结构差异的图（左亮右暗），保证 aHash 稳定
func makeTestImage(t *testing.T, w, h int, invert bool) image.Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			var v uint8 = 30
			if (x < w/2) != invert {
				v = 220
			}
			img.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return img
}

// 同一张图不同编码（jpg 质量 60 / 95、不同尺寸）→ 感知哈希相同，内容哈希不同
func TestPerceptualHashStableAcrossEncoding(t *testing.T) {
	dir := t.TempDir()
	img := makeTestImage(t, 400, 300, false)

	var buf1, buf2 bytes.Buffer
	if err := jpeg.Encode(&buf1, img, &jpeg.Options{Quality: 60}); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&buf2, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	p1 := filepath.Join(dir, "a1.jpg")
	p2 := filepath.Join(dir, "a2.jpg")
	if err := os.WriteFile(p1, buf1.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2, buf2.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	h1, err := perceptualHash(p1)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := perceptualHash(p2)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("同一张图不同质量编码，感知哈希应该相同：%s vs %s", h1, h2)
	}

	c1, _ := fileHash(p1)
	c2, _ := fileHash(p2)
	if c1 == c2 {
		t.Error("内容哈希本来就该不同（测试前提）")
	}
}

// 内容不同的图 → 感知哈希不同（不能误杀）
func TestPerceptualHashDiffersForDifferentImages(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	b := filepath.Join(dir, "b.png")

	var bufa, bufb bytes.Buffer
	if err := png.Encode(&bufa, makeTestImage(t, 200, 200, false)); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&bufb, makeTestImage(t, 200, 200, true)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, bufa.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, bufb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	ha, _ := perceptualHash(a)
	hb, _ := perceptualHash(b)
	if ha == hb {
		t.Errorf("左右反转的图应该得到不同感知哈希（都是 %s）", ha)
	}
}

// 非图片文件：返回错误，调用方只走内容哈希
func TestPerceptualHashNonImage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(p, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := perceptualHash(p); err == nil {
		t.Error("非图片应该报错")
	}
}

// 端到端：同一张图换名字重编码再发 → 被拦住
func TestSendBlocksSameImageDifferentBytes(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out", "anime")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	img := makeTestImage(t, 300, 300, false)

	var q60, q90 bytes.Buffer
	_ = jpeg.Encode(&q60, img, &jpeg.Options{Quality: 60})
	_ = jpeg.Encode(&q90, img, &jpeg.Options{Quality: 90})

	// 模型常见行为：同一张图存成两个文件名
	p1 := filepath.Join(out, "15.jpg")
	p2 := filepath.Join(out, "17.jpg")
	_ = os.WriteFile(p1, q60.Bytes(), 0o644)
	_ = os.WriteFile(p2, q90.Bytes(), 0o644)

	fs := &fakeSender{}
	ctx := WithFileSender(WithImageSender(context.Background(), fs), fs)
	tool := newSendToolWith(dir, NewSentLedger(""))

	if _, err := tool.Execute(ctx, []byte(`{"path":"out/anime/15.jpg"}`)); err != nil {
		t.Fatal(err)
	}
	if fs.imagePath == "" {
		t.Fatal("第一张应该发出去")
	}

	fs2 := &fakeSender{}
	ctx2 := WithFileSender(WithImageSender(context.Background(), fs2), fs2)
	msg, err := tool.Execute(ctx2, []byte(`{"path":"out/anime/17.jpg"}`))
	if err != nil {
		t.Fatal(err)
	}
	if fs2.imagePath != "" {
		t.Error("画面相同的图不该再发一次")
	}
	t.Logf("拦截提示：%s", msg)
}
