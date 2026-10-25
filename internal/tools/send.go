package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"little-robot/internal/schema"
)

/*
send：把工作区里的文件发给用户（ch30 合并 send_image + send_file）。

为什么合并：两个工具是同一个意图（"把本地文件交付给用户"），
却要模型预判容器——12MB 的 png 走 send_image 会撞飞书 10MB 上限，
pdf 走 send_image 会按"图片"上传。路由规则其实是确定的
（扩展名 + 大小），确定性的事不该让模型猜，猜错就是白烧一轮。

路由规则（工具内部决定，模型无感知）：
- 图片扩展名（jpg/jpeg/png/webp/gif）且 ≤10MB → image 消息（内联预览，体验最好）
- 其他 → file 消息（pdf/zip/mp4/doc，≤30MB，飞书 file_type 枚举由扩展名推断）

飞书侧的 ImageSender / FileSender 两个接口保持拆分：那是**外壳**的职责
（飞书 image 和 file 是两条上传 API），tools 层只负责选哪条。
*/

// ImageSender 由外壳实现：飞书会真的把图片发出去
type ImageSender interface {
	SendImage(ctx context.Context, absPath string) error
}

// FileSender 由外壳实现：把任意文件（pdf/zip/mp4...）发到飞书。
// 与 ImageSender 同一个套路：飞书不能直接发本地文件，
// 必须先走 /im/v1/files 上传换 file_key，再发 file 消息。
type FileSender interface {
	SendFile(ctx context.Context, absPath, fileName, fileType string) error
}

type imageSenderKey struct{}
type fileSenderKey struct{}

// WithImageSender 把 sender 塞进 ctx。
// 为什么用 ctx 而不是工具字段：sender 是"这一次运行"的身份（回复哪条消息），
// 而工具实例是进程级的，两者生命周期不同。
func WithImageSender(ctx context.Context, s ImageSender) context.Context {
	return context.WithValue(ctx, imageSenderKey{}, s)
}

func imageSenderFrom(ctx context.Context) ImageSender {
	s, _ := ctx.Value(imageSenderKey{}).(ImageSender)
	return s
}

// WithFileSender 与 WithImageSender 同款：sender 是本次运行的身份，工具实例是进程级的
func WithFileSender(ctx context.Context, s FileSender) context.Context {
	return context.WithValue(ctx, fileSenderKey{}, s)
}

func fileSenderFrom(ctx context.Context) FileSender {
	s, _ := ctx.Value(fileSenderKey{}).(FileSender)
	return s
}

// 飞书两条通道的硬上限：image 消息 10MB，file 消息 30MB
const (
	imageMaxBytes = 10 << 20
	fileMaxBytes  = 30 << 20
)

var _ BaseTool = &SendTool{}

type SendTool struct {
	workDir string
}

func NewSendTool(workDir string) *SendTool {
	return &SendTool{workDir: workDir}
}

func (t *SendTool) Name() string { return "send" }

func (t *SendTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: `把工作区里的文件发送给用户，消息形态由工具自动决定，不用你选：
图片（jpg/png/webp/gif）且 ≤10MB 走图片消息（用户内联预览）；
其他格式（PDF/zip/mp4/doc）或超大的图走文件消息（≤30MB）。
只发"已经在工作区里的文件"；外部图片先用 fetch_image 下载，文本内容直接回复不要发文件。`,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "相对工作区的文件路径，例如 out/chart.png、out/hentai/12345/12345.zip",
				},
				"name": map[string]interface{}{
					"type":        "string",
					"description": "发给用户的文件名，必须带正确后缀（.pdf/.zip/.jpg），不填就用磁盘上的文件名。建议带上下文，例如「伊莉雅 本子 第1部分（共2部分）.pdf」；不要用 1/2 这种带斜杠的写法",
				},
			},
			"required": []string{"path"},
		},
	}
}

// isImageExt 图片扩展名白名单：这些走飞书 image 通道才有内联预览
func isImageExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return true
	}
	return false
}

// inferFileType 按扩展名映射飞书 file_type 枚举；不在枚举里的用 stream 兜底
func inferFileType(absPath string) string {
	switch strings.ToLower(filepath.Ext(absPath)) {
	case ".pdf":
		return "pdf"
	case ".mp4":
		return "mp4"
	case ".doc", ".docx":
		return "doc"
	case ".xls", ".xlsx":
		return "xls"
	case ".ppt", ".pptx":
		return "ppt"
	case ".opus", ".ogg":
		return "opus"
	default:
		return "stream" // zip / epub / cbz / txt / html...
	}
}

func (t *SendTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%w", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path 不能为空")
	}

	// 复用沙箱：不允许把工作区外的文件发出去
	dir := effectiveWorkDir(ctx, t.workDir)
	full, err := resolvePath(dir, in.Path)
	if err != nil {
		return "", err
	}

	st, err := os.Stat(full)
	if err != nil {
		return "", fmt.Errorf("文件不存在：%w", err)
	}
	if st.Size() == 0 {
		return "", fmt.Errorf("空文件（0 字节），飞书不接受：%s", in.Path)
	}
	if st.Size() > fileMaxBytes {
		return "", fmt.Errorf("文件 %d 字节超过飞书 30MB 上限，考虑压缩或分片", st.Size())
	}

	// 展示名要"能用"：模型给的 name 经常缺后缀、带斜杠（真实事故：
	// name="…PART.1）1/2" → 没有 .pdf 手机认不出，斜杠被客户端当路径切开只剩 "2"）
	fileName := sanitizeFileName(in.Name, full)

	// 自动路由：图片优先走 image 通道（内联预览，体验最好）；
	// 没有 ImageSender 的环境（比如只挂了 FileSender 的外壳）落到 file 通道兜底
	if isImageExt(full) && st.Size() <= imageMaxBytes {
		if sender := imageSenderFrom(ctx); sender != nil {
			if err := sender.SendImage(ctx, full); err != nil {
				return "", fmt.Errorf("发送图片失败：%w", err)
			}
			return fmt.Sprintf("图片 %s 已发送给用户（图片消息，用户可直接预览）", in.Path), nil
		}
	}

	sender := fileSenderFrom(ctx)
	if sender == nil {
		return "", fmt.Errorf("当前环境不支持发送文件（只有飞书等外壳支持）")
	}
	fileType := inferFileType(full)
	if err := sender.SendFile(ctx, full, fileName, fileType); err != nil {
		return "", fmt.Errorf("发送文件失败：%w", err)
	}
	why := "格式不是图片"
	switch {
	case isImageExt(full) && st.Size() > imageMaxBytes:
		why = fmt.Sprintf("图片超过 10MB（%d 字节），降级为文件消息", st.Size())
	case isImageExt(full):
		why = "环境没有图片通道，降级为文件消息"
	}
	return fmt.Sprintf("文件 %s（%s，%d 字节）已发送给用户（%s）", in.Path, fileType, st.Size(), why), nil
}

// maxFileNameRunes 展示名长度上限：够放标题+分片信息，又不至于被飞书截断
const maxFileNameRunes = 80

// sanitizeFileName 把模型给的展示名整理成真正能用的文件名。
//
// 三条规则都来自真实事故：
//  1. 去路径分隔符：name="…1/2" 会让客户端把它当路径，用户只看到 "2"；
//  2. 保住扩展名：没有 .pdf，手机端根本不知道用什么打开；
//  3. 兜底：模型给的名字为空/全是非法字符时，用磁盘上的原名。
func sanitizeFileName(name, absPath string) string {
	realExt := filepath.Ext(absPath)

	base := strings.TrimSpace(name)
	base = strings.Map(func(r rune) rune {
		switch r {
		// 路径分隔符 + Windows/飞书客户端不接受的字符，统一换成短横线
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\n', '\r', '\t', 0:
			return '-'
		// 书名号在文件名里合法，但很占地方（手机上会被折行），直接去掉
		case '《':
			return -1
		case '》':
			return ' '
		}
		return r
	}, base)
	// 连续短横线/空格压成一个，首尾的短横线去掉（"a//b" → "a-b"）
	for strings.Contains(base, "--") {
		base = strings.ReplaceAll(base, "--", "-")
	}
	for strings.Contains(base, "  ") {
		base = strings.ReplaceAll(base, "  ", " ")
	}
	base = strings.Trim(base, "- ")
	base = strings.TrimSpace(base)

	// 过短的名字（"2"、"a"）几乎肯定是模型写残了，用磁盘原名更靠谱
	if len([]rune(base)) < 3 {
		return filepath.Base(absPath)
	}

	// 扩展名对齐：名字自带的后缀和真实文件不一致时，以真实文件为准
	if strings.EqualFold(filepath.Ext(base), realExt) {
		if base == "" {
			return filepath.Base(absPath)
		}
		return base
	}
	// 名字里可能带了个假后缀（.zip 其实发的是 pdf），去掉再补真实后缀
	if other := filepath.Ext(base); other != "" && len(other) <= 6 {
		base = strings.TrimSuffix(base, other)
	}
	base = strings.TrimSpace(base)

	// 太长就截断，但要把扩展名的位置留出来
	ext := realExt
	if ext == "" {
		ext = filepath.Ext(absPath)
	}
	limit := maxFileNameRunes - len([]rune(ext))
	r := []rune(base)
	if limit > 0 && len(r) > limit {
		base = string(r[:limit])
	}
	return base + ext
}
