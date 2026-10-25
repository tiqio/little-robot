package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"little-robot/internal/schema"
)

/*
发送图片的能力必须由外壳提供（飞书会真的发出去，终端发不了）。
接口定义在消费方（tools），实现放在提供方（feishu），
这样 tools 不用反向依赖 feishu，也不会和 engine 形成循环。
这是 ch17 AgentRunner 的同一个套路。
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

var _ BaseTool = &SendImageTool{}

type SendImageTool struct {
	workDir string
}

func NewSendImageTool(workDir string) *SendImageTool {
	return &SendImageTool{workDir: workDir}
}

func (t *SendImageTool) Name() string { return "send_image" }

func (t *SendImageTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "把工作区里的图片文件发送给用户。适用于把生成好的图表、截图、示意图发给用户。" +
			"只支持 png/jpg/webp/gif 等图片；发文本请直接回复，不要调用本工具。" +
			"文件（PDF/zip/mp4/doc）请用 send_file，不要用本工具。" +
			"特别的，如果本地没有图片但是我想要你给我图片，可以去网站上搜索并下载，包括但不限于wallpaperaccess和images.hdqwalls.com等",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "相对工作区的图片路径，例如 out/chart.png",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (t *SendImageTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%w", err)
	}

	sender := imageSenderFrom(ctx)
	if sender == nil {
		return "", fmt.Errorf("当前环境不支持发送图片（只有飞书等外壳支持）")
	}

	// 复用沙箱：不允许把工作区外的文件当图片发出去
	dir := effectiveWorkDir(ctx, t.workDir)
	full, err := resolvePath(dir, in.Path)
	if err != nil {
		return "", err
	}

	if err := sender.SendImage(ctx, full); err != nil {
		return "", fmt.Errorf("发送图片失败：%w", err)
	}
	return fmt.Sprintf("图片 %s 已发送给用户", in.Path), nil
}
