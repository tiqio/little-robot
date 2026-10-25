package tools

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pageJPEG(t *testing.T, w, h int, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// 加一点噪声，避免纯色 JPEG 小到测不出体积差异
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{shade, uint8(x % 256), uint8(y % 256), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 装得下就一个文件
func TestWritePDFPartsSingle(t *testing.T) {
	dir := t.TempDir()
	imgs := [][]byte{pageJPEG(t, 400, 600, 10), pageJPEG(t, 400, 600, 90)}

	parts, err := writePDFParts(dir, "123", imgs, 25<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("应该只有 1 个 PDF，实际 %d", len(parts))
	}
	if filepath.Base(parts[0]) != "123.pdf" {
		t.Errorf("文件名应该是 123.pdf，实际 %s", filepath.Base(parts[0]))
	}
}

// 超限就拆成 part1/part2，且每份都装得下、页序不丢
func TestWritePDFPartsSplits(t *testing.T) {
	dir := t.TempDir()
	imgs := make([][]byte, 4)
	for i := range imgs {
		imgs[i] = pageJPEG(t, 300, 400, uint8(i*50))
	}
	// 先算整本多大，再把上限压到一半，逼它拆
	full, err := writePDFParts(dir, "probe", imgs, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(full[0])
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(full[0])

	limit := fi.Size() / 2
	parts, err := writePDFParts(dir, "456", imgs, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 2 {
		t.Fatalf("应该拆成多个，实际 %d 个", len(parts))
	}
	for _, p := range parts {
		s, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if s.Size() > limit {
			t.Errorf("%s 超过上限：%d > %d", filepath.Base(p), s.Size(), limit)
		}
		data, _ := os.ReadFile(p)
		if !bytes.HasPrefix(data, []byte("%PDF")) {
			t.Errorf("%s 不是合法 PDF", filepath.Base(p))
		}
	}
	// 名字要能看出是分片
	if !strings.Contains(filepath.Base(parts[0]), "-part1") {
		t.Errorf("分片命名不对：%s", filepath.Base(parts[0]))
	}
}

// 官方 zip → 按文件名排序取出图片页
func TestZipPageImagesSorted(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// 故意乱序写入，验证读出来是按名字排序的
	for _, name := range []string{"g/010.jpg", "g/002.jpg", "g/001.jpg"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		// 用文件名当内容，方便断言顺序
		if _, err := w.Write([]byte(name)); err != nil {
			t.Fatal(err)
		}
	}
	// 目录和非图片条目要被忽略
	if _, err := zw.Create("g/"); err != nil {
		t.Fatal(err)
	}
	if w, err := zw.Create("g/info.txt"); err == nil {
		_, _ = w.Write([]byte("meta"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	imgs, err := zipPageImages(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"g/001.jpg", "g/002.jpg", "g/010.jpg"}
	if len(imgs) != len(want) {
		t.Fatalf("应该取到 %d 张，实际 %d", len(want), len(imgs))
	}
	for i, w := range want {
		if string(imgs[i]) != w {
			t.Errorf("第 %d 张 = %q，期望 %q（应按文件名排序）", i, string(imgs[i]), w)
		}
	}
}

// 没有图片的 zip 要报错，不能生成空 PDF
func TestZipPageImagesNoImages(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	_, _ = w.Write([]byte("hello"))
	_ = zw.Close()

	if _, err := zipPageImages(buf.Bytes()); err == nil {
		t.Error("没有图片的 zip 应该报错")
	}
}
