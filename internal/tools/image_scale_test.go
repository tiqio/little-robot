package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makePNG 生成一张 w×h 的随机噪声 PNG（噪声压缩率低，能稳定造出"大图"）
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(42))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(r.Int()), uint8(r.Int()), uint8(r.Int()), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 大图降采样：2000×1500 → 最长边 768，且是合法 JPEG
func TestDownscaleToJPEG(t *testing.T) {
	data := makePNG(t, 2000, 1500)
	scaled, ok := downscaleToJPEG(data, carrierMaxDim)
	if !ok {
		t.Fatal("大图应该被降采样，结果走了回落")
	}
	if len(scaled) >= len(data) {
		t.Errorf("降采样后应该更小：原 %d → 新 %d", len(data), len(scaled))
	}
	img, format, err := image.Decode(bytes.NewReader(scaled))
	if err != nil {
		t.Fatalf("降采样结果不是合法图片：%v", err)
	}
	if format != "jpeg" {
		t.Errorf("期望 jpeg，实际 %s", format)
	}
	b := img.Bounds()
	if b.Dx() > carrierMaxDim || b.Dy() > carrierMaxDim {
		t.Errorf("最长边应该 <= %d，实际 %dx%d", carrierMaxDim, b.Dx(), b.Dy())
	}
	// 比例保持：2000:1500 = 4:3
	if b.Dx()*3 != b.Dy()*4 {
		t.Errorf("比例应该保持 4:3，实际 %dx%d", b.Dx(), b.Dy())
	}
}

// 小图不值得缩：本来就不大，返回 false 让调用方走原图
func TestDownscaleSmallPassthrough(t *testing.T) {
	data := makePNG(t, 400, 300)
	if _, ok := downscaleToJPEG(data, carrierMaxDim); ok {
		t.Error("小于 maxDim 的图不应该被缩放")
	}
}

// fileToDataURL：小图走原格式，大图走 768px JPEG
func TestFileToDataURLScalesBig(t *testing.T) {
	dir := t.TempDir()

	small := makePNG(t, 300, 200) // <256KB
	smallPath := filepath.Join(dir, "small.png")
	if err := os.WriteFile(smallPath, small, 0o644); err != nil {
		t.Fatal(err)
	}
	url, err := fileToDataURL(smallPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "data:image/png;") {
		t.Errorf("小图应该保持 png，实际 %s", url[:30])
	}

	big := makePNG(t, 1500, 1000) // 噪声 PNG ≈ 4.5MB > 256KB
	bigPath := filepath.Join(dir, "big.png")
	if err := os.WriteFile(bigPath, big, 0o644); err != nil {
		t.Fatal(err)
	}
	url, err = fileToDataURL(bigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "data:image/jpeg;") {
		t.Errorf("大图应该降成 jpeg，实际 %s", url[:30])
	}
	if len(url) >= len(big)*4/3 {
		t.Errorf("data URL 应该比原图 base64 小：原图 %d 字节，url %d", len(big), len(url))
	}
}
