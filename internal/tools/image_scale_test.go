package tools

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
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

// tinyWebP 是一张合法的 2×2 无损 WebP（34 字节）。
// 它复现了线上事故：llama.cpp 对这个格式回
// `'url' field must be a base64 encoded image.`，整个任务直接 400。
const tinyWebP = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="

func tinyWebPBytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(tinyWebP)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 大图：缩到最长边 768，保持比例，输出 jpeg
func TestCarrierImageScalesBig(t *testing.T) {
	data := makePNG(t, 2000, 1500)
	out, err := carrierImage(data, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Fatal("大图应该被处理，结果原样透传")
	}
	if len(out) >= len(data) {
		t.Errorf("处理完应该更小：原 %d → 新 %d", len(data), len(out))
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("结果不是合法图片：%v", err)
	}
	if format != "jpeg" {
		t.Errorf("期望 jpeg，实际 %s", format)
	}
	b := img.Bounds()
	if b.Dx() > carrierMaxDim || b.Dy() > carrierMaxDim {
		t.Errorf("最长边应该 <= %d，实际 %dx%d", carrierMaxDim, b.Dx(), b.Dy())
	}
	if b.Dx()*3 != b.Dy()*4 {
		t.Errorf("比例应该保持 4:3，实际 %dx%d", b.Dx(), b.Dy())
	}
}

// 小 png 原样透传（无损更清晰，不值得重编码）
func TestCarrierImageSmallPNGPassthrough(t *testing.T) {
	data := makePNG(t, 300, 200)
	out, err := carrierImage(data, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("小 png 应该原样透传，却返回了 %d 字节", len(out))
	}
}

// 根因回归：webp 必须转成 jpeg，否则本地后端 400
func TestCarrierImageWebPToJPEG(t *testing.T) {
	out, err := carrierImage(tinyWebPBytes(t), "image/webp")
	if err != nil {
		t.Fatalf("webp 应该能解码：%v", err)
	}
	if out == nil {
		t.Fatal("webp 必须转码（原样透传就是线上那个 400）")
	}
	_, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" {
		t.Errorf("webp 转码后应该是 jpeg，实际 %s", format)
	}
}

// gif 也转 jpeg（各家后端对动图支持不一，给模型"看"第一帧就够）
func TestCarrierImageGIFToJPEG(t *testing.T) {
	// 用标准库编码一张真 GIF：手写字节容易造出非法帧
	img := image.NewPaletted(image.Rect(0, 0, 4, 3), color.Palette{
		color.RGBA{255, 0, 0, 255},
		color.RGBA{0, 0, 255, 255},
	})
	for x := 0; x < 4; x++ {
		for y := 0; y < 3; y++ {
			img.SetColorIndex(x, y, uint8((x+y)%2))
		}
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}

	out, err := carrierImage(buf.Bytes(), "image/gif")
	if err != nil {
		t.Fatalf("gif 应该能解码：%v", err)
	}
	if out == nil {
		t.Fatal("gif 应该转码成 jpeg")
	}
	if _, format, err := image.Decode(bytes.NewReader(out)); err != nil || format != "jpeg" {
		t.Errorf("gif 转码后应该是 jpeg，实际 %s (%v)", format, err)
	}
}

// 坏图要在本地就拦下：送到后端只会变成 400 把任务跑死
func TestCarrierImageCorrupt(t *testing.T) {
	if _, err := carrierImage([]byte("this is not an image"), "image/png"); err == nil {
		t.Error("坏图应该报错，而不是放行")
	}
}

// 体积压线：尺寸不大但 PNG 很重（噪声），也要重编码压小
func TestCarrierImageHeavyPNG(t *testing.T) {
	data := makePNG(t, 700, 700) // 噪声 PNG ≈ 1.4MB > 256KB
	if len(data) <= carrierMaxBytes {
		t.Skipf("测试数据不够重（%d 字节），跳过", len(data))
	}
	out, err := carrierImage(data, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Fatal("重图应该被重编码")
	}
	if len(out) >= len(data) {
		t.Errorf("重编码后应该更小：%d → %d", len(data), len(out))
	}
}

// fileToDataURL：webp 出来必须是 jpeg 的 data URL（用户事故的端到端回归）
func TestFileToDataURLWebPBecomesJPEG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tiny.webp")
	if err := os.WriteFile(path, tinyWebPBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}

	url, err := fileToDataURL(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Errorf("webp 应该转成 jpeg data URL，实际前 40 字节：%q", url[:min(40, len(url))])
	}
}

// 坏图文件：fileToDataURL 报错，attachImage 会退化成"没挂上"而不是 400
func TestFileToDataURLCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fileToDataURL(path); err == nil {
		t.Error("坏图文件应该报错")
	}
}
