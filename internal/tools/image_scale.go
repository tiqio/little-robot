package tools

import (
	"bytes"
	"image"
	_ "image/gif" // 注册到 image.Decode：GIF 取第一帧
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp" // 注册到 image.Decode：webp

	"golang.org/x/image/draw" // BiLinear 缩放 + Over 混合算子
)

// carrierMaxDim 图片进模型上下文的最大边长（像素）。
// 原图留在磁盘上（send 发原图、view_image 可重新看），进上下文的只是"缩略图"。
// 3MB 的 PNG 当 base64 是几万 token，对 14 tok/s 的本地模型是灾难；768px JPEG 约百来 KB。
const carrierMaxDim = 768

// carrierMinBytes 小于这个值的图直接走原格式，不值得解码重编码
const carrierMinBytes = 256 << 10

// downscaleToJPEG 把图片缩到最长边 maxDim，重编码成 JPEG q80。
// 返回 false = 不值得缩（解码失败 / 本来就不大），调用方回落原图。
// GIF 只保留第一帧——给模型"看"的用途，动图信息在静态帧里就够了。
func downscaleToJPEG(data []byte, maxDim int) ([]byte, bool) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return nil, false
	}
	scale := float64(maxDim) / float64(maxDimOf(w, h))
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func maxDimOf(a, b int) int {
	if a > b {
		return a
	}
	return b
}
