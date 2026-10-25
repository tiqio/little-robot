package tools

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif" // 注册到 image.Decode：GIF 取第一帧
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"   // BiLinear 缩放 + Over 混合算子
	_ "golang.org/x/image/webp" // 注册到 image.Decode：webp
)

// carrierMaxDim 图片进模型上下文的最大边长（像素）。
// 原图留在磁盘上（send 发原图、view_image 可重新看），进上下文的只是"缩略图"。
// 3MB 的 PNG 当 base64 是几万 token，对 14 tok/s 的本地模型是灾难；768px JPEG 约百来 KB。
const carrierMaxDim = 768

// carrierMaxBytes 超过这个体积就重编码压小（同尺寸但无损压得很差的 PNG 很常见）
const carrierMaxBytes = 256 << 10

// carrierJPEGQuality 缩略图质量：80 在"看得清"和"体积小"之间够用
const carrierJPEGQuality = 82

// carrierImage 把一张图整理成"能安全进模型上下文"的字节。
//
// 三条规则，都来自真实的 400：
//  1. 只放行 png/jpeg——本地 llama.cpp 对 webp 直接回
//     `'url' field must be a base64 encoded image.`，gif 能过但各家后端不一，
//     所以 webp/gif 一律转 jpeg（gif 取第一帧，给模型"看"够用）；
//  2. 先解码一次做校验——下载回来的图可能是坏文件/半截包，送过去就是 400，
//     还不如在这里就报清楚，attachImage 会退化成"这张图没挂上"；
//  3. 太大（像素超 768 或体积超 256KB）就缩放重编码。
//
// 返回 newBytes 为空 表示"原样发送即可"。
func carrierImage(data []byte, ctype string) (newBytes []byte, err error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("解码图片失败（文件可能损坏）：%w", err)
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	tooBig := maxDimOf(w, h) > carrierMaxDim
	tooHeavy := len(data) > carrierMaxBytes
	exotic := ctype != "image/jpeg" && ctype != "image/png"

	if !tooBig && !tooHeavy && !exotic {
		return nil, nil // png/jpeg 且又小又轻：原样最清晰，不折腾
	}

	nw, nh := w, h
	if tooBig {
		scale := float64(carrierMaxDim) / float64(maxDimOf(w, h))
		nw = max(1, int(float64(w)*scale))
		nh = max(1, int(float64(h)*scale))
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: carrierJPEGQuality}); err != nil {
		return nil, fmt.Errorf("重编码图片失败：%w", err)
	}
	return buf.Bytes(), nil
}

func maxDimOf(a, b int) int {
	if a > b {
		return a
	}
	return b
}
