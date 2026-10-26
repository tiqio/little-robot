package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"os"

	"golang.org/x/image/draw"
)

// perceptualHash 图片的"感知哈希"（aHash）。
//
// 为什么需要它：同一张图从不同 CDN 下载、或被重新编码后，**字节完全不同**，
// 内容 SHA-256 就变了，去重失效——用户看到的就是"发了两张一样的图"。
// 做法：缩到 8×8 灰度，跟均值比较得到 64 bit，图片内容相同（即使重压缩）→ 哈希相同。
//
// 只对能解码的图片生效；其它文件返回空串，调用方只做内容哈希。
func perceptualHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("不是可解码的图片：%w", err)
	}

	const n = 8
	dst := image.NewGray(image.Rect(0, 0, n, n))
	// 平均插值就够：aHash 只要亮度分布，不需要锐利细节
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)

	var sum int
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			sum += int(dst.GrayAt(x, y).Y)
		}
	}
	avg := sum / (n * n)

	// 64 bit 拼成 16 位十六进制
	var bits [n * n]byte
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if int(dst.GrayAt(x, y).Y) > avg {
				bits[y*n+x] = 1
			}
		}
	}
	h := sha256.New()
	h.Write(bits[:])
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
