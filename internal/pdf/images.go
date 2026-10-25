// Package pdf 用标准库拼 PDF：每页一张图，铺满页面，不加密。
//
// 为什么自己写而不是引第三方库：需求只有"把一堆图按顺序拼成能翻的 PDF"，
// 用到的 PDF 特性极少——Catalog / Pages / Page / XObject(Image) / 内容流，
// 一百来行就够，还不用背一个几 MB 的依赖。
// 为什么不加密：飞书/手机要能直接预览，加密的 PDF 反而打不开。
package pdf

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"

	"golang.org/x/image/draw"

	_ "golang.org/x/image/webp"
)

// pageWidthPt 页面宽度（点）。595pt ≈ A4 宽，手机/电脑阅读器都按宽度铺满。
// 只锁宽度、高度按图片比例算：竖图得到长页，横图得到宽页，图不会被拉伸。
const pageWidthPt = 595.0

// imageQuality 内嵌 JPEG 的质量：88 肉眼无损，体积大约只有 PNG 的 1/3~1/5
const imageQuality = 88

// maxPageDim 单页最长边上限（像素）。本子原图常见 1200~1800，一般不动；
// 遇到 4000px 的扫描图才缩，用来控制 PDF 总体积（飞书单文件 30MB）
const maxPageDim = 2000

// BuildImages 把本地图片文件按顺序拼成一个 PDF。
func BuildImages(dst string, files []string) error {
	images := make([][]byte, 0, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("读取图片失败（%s）：%w", f, err)
		}
		images = append(images, data)
	}
	return BuildImagesFromMemory(dst, images)
}

// BuildImagesFromMemory 同 BuildImages，但图片已经在内存里
// （nhentai 官方 zip 一条请求就能拿全本，不用再落盘 N 个文件）。
func BuildImagesFromMemory(dst string, images [][]byte) error {
	if len(images) == 0 {
		return fmt.Errorf("一张图都没有，没法生成 PDF")
	}

	var pages []page
	for i, data := range images {
		p, err := preparePage(data)
		if err != nil {
			return fmt.Errorf("第 %d 页处理失败：%w", i+1, err)
		}
		pages = append(pages, p)
	}

	var out bytes.Buffer
	if err := writePDF(&out, pages); err != nil {
		return err
	}
	if err := os.WriteFile(dst, out.Bytes(), 0o644); err != nil {
		return fmt.Errorf("写 PDF 失败：%w", err)
	}
	return nil
}

// page 是一页的最终形态：JPEG 字节 + 原始像素尺寸
type page struct {
	jpeg     []byte
	pxWidth  int
	pxHeight int
}

// preparePage 把任意支持的图片解码、铺白底（去 alpha）、重编码成 RGB JPEG。
// 统一重编码的好处：CMYK / 灰度 / 带透明的图都变成最兼容的 DeviceRGB JPEG，
// 内嵌时不需要为每种色彩空间单独写分支。
func preparePage(data []byte) (page, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return page{}, fmt.Errorf("解码图片失败：%w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 {
		return page{}, fmt.Errorf("图片尺寸非法：%dx%d", w, h)
	}

	// 画到白底 RGBA 上：PDF 里的 JPEG 没有 alpha，透明区域必须是白色，
	// 否则渲染器会把它当黑色，整页脏。
	// 用 draw 做一次整块合成（逐像素 Set 在 1200×1700 上要跑两百万次，太慢）
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)

	// 超长边才缩：控制单页体积，避免一本几百页的 PDF 撑爆飞书 30MB 上限
	if maxDimOf(w, h) > maxPageDim {
		scale := float64(maxPageDim) / float64(maxDimOf(w, h))
		nw := max(1, int(float64(w)*scale))
		nh := max(1, int(float64(h)*scale))
		resized := image.NewRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(resized, resized.Bounds(), dst, dst.Bounds(), draw.Over, nil)
		dst, w, h = resized, nw, nh
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: imageQuality}); err != nil {
		return page{}, fmt.Errorf("编码 JPEG 失败：%w", err)
	}
	return page{jpeg: buf.Bytes(), pxWidth: w, pxHeight: h}, nil
}

// writePDF 生成完整 PDF（含 xref 表）。
//
// 对象编号约定：
//
//	1 = Catalog
//	2 = Pages
//	每页 3 个对象：Page / Contents(内容流) / Image(XObject)
func writePDF(out *bytes.Buffer, pages []page) error {
	// offsets[i] = 第 i 个对象（1 起）在文件里的字节偏移，xref 表要用
	offsets := make(map[int]int)
	writeObj := func(num int, body string) {
		offsets[num] = out.Len()
		fmt.Fprintf(out, "%d 0 obj\n%s\nendobj\n", num, body)
	}

	out.WriteString("%PDF-1.4\n")
	// 二进制标记：告诉下游"这个文件里有二进制数据"，避免被当文本转换
	out.Write([]byte{'%', 0xe2, 0xe3, 0xcf, 0xd3, '\n'})

	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")

	kids := make([]string, 0, len(pages))
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObjNum(i)))
	}
	writeObj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>",
		strings.Join(kids, " "), len(pages)))

	for i, p := range pages {
		// 页面尺寸：宽度固定 595pt，高度按比例（保留图片原比例，绝不拉伸）
		pageH := pageWidthPt * float64(p.pxHeight) / float64(p.pxWidth)
		content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", pageWidthPt, pageH)
		writeObj(pageObjNum(i), fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] "+
				"/Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
			pageWidthPt, pageH, imageObjNum(i), contentObjNum(i)))

		writeObj(contentObjNum(i), fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))

		// 图片对象：DCTDecode = 直接塞 JPEG 原始字节，PDF 阅读器自己解
		offsets[imageObjNum(i)] = out.Len()
		fmt.Fprintf(out, "%d 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d "+
			"/ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n",
			imageObjNum(i), p.pxWidth, p.pxHeight, len(p.jpeg))
		out.Write(p.jpeg)
		out.WriteString("\nendstream\nendobj\n")
	}

	// xref：每个对象一行，格式固定 20 字节（10位偏移 + 5位世代 + n/f + 2字节EOL）
	xrefPos := out.Len()
	total := 2 + 3*len(pages) // 对象 0（free）+ Catalog + Pages + 每页 3 个
	fmt.Fprintf(out, "xref\n0 %d\n", total+1)
	out.WriteString("0000000000 65535 f \n")
	for i := 1; i <= total; i++ {
		fmt.Fprintf(out, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", total+1, xrefPos)
	return nil
}

// 对象编号：Catalog=1, Pages=2，然后每页 3 个
func pageObjNum(i int) int    { return 3 + i*3 }
func contentObjNum(i int) int { return 4 + i*3 }
func imageObjNum(i int) int   { return 5 + i*3 }

func maxDimOf(a, b int) int {
	if a > b {
		return a
	}
	return b
}
