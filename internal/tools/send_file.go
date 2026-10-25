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
send_file：把非图片的多媒体文件发到飞书（PDF、zip、mp4、doc...）。
send_image 只能发 jpg/png/webp/gif，爬虫抓到的 PDF / 本子打包的 zip 走这里。

飞书 file 消息的 file_type 是枚举（opus/mp4/pdf/doc/xls/ppt/stream），
stream = "以上都不是"的兜底类型，zip/epub/cbz/txt 都用它。
限制：≤30MB（飞书上限定死的），不允许空文件。
*/

const fileMaxBytes = 30 << 20

var _ BaseTool = &SendFileTool{}

type SendFileTool struct {
	workDir string
}

func NewSendFileTool(workDir string) *SendFileTool {
	return &SendFileTool{workDir: workDir}
}

func (t *SendFileTool) Name() string { return "send_file" }

func (t *SendFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "把工作区里的文件（PDF、zip、mp4、doc 等）发送给用户。" +
			"fetch_page 抓到的 PDF、fetch_hentai 打包的本子 zip 都走这里。" +
			"图片请用 send_image，别用本工具发 jpg/png。" +
			"限制：≤30MB；file 名建议带点上下文，例如「xxx 的扫描版.pdf」。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "相对工作区的文件路径，例如 out/crawl/example.com/report.pdf",
				},
				"name": map[string]interface{}{
					"type":        "string",
					"description": "发给用户的文件名（带后缀），不填就用磁盘上的文件名",
				},
			},
			"required": []string{"path"},
		},
	}
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

func (t *SendFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
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

	sender := fileSenderFrom(ctx)
	if sender == nil {
		return "", fmt.Errorf("当前环境不支持发送文件（只有飞书等外壳支持）")
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

	fileName := strings.TrimSpace(in.Name)
	if fileName == "" {
		fileName = filepath.Base(full)
	}
	fileType := inferFileType(full)

	if err := sender.SendFile(ctx, full, fileName, fileType); err != nil {
		return "", fmt.Errorf("发送文件失败：%w", err)
	}
	return fmt.Sprintf("文件 %s（%s，%d 字节）已发送给用户", in.Path, fileType, st.Size()), nil
}
