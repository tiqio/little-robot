package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"little-robot/internal/schema"
	"os"
	"path/filepath"
	"strings"
)

var (
	_ BaseTool = &ReadFileTool{}
)

const maxReadBytes = 8000

type ReadFileTool struct {
	workDir string
}

func NewReadFileTool(workDir string) *ReadFileTool {
	return &ReadFileTool{
		workDir: workDir,
	}
}

func (t *ReadFileTool) Name() string {
	return "read_file"
}

func (t *ReadFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "读取指定路径的文本文件内容，路径必须是工作区的相对路径",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "相对工作区的文件路径，例如cmd/robot/main.go",
				},
			},
			"required": []string{"path"},
		},
	}
}

type readFileArgs struct {
	Path string `json:"path"`
}

func (t *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in readFileArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%w", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path不能为空")
	}

	full, err := t.resolve(in.Path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(full)
	if err != nil {
		return "", fmt.Errorf("读取%q失败：%w", in.Path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q是目录，不是文件", in.Path)
	}

	content, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("读取%q失败：%w", in.Path, err)
	}

	if len(content) > maxReadBytes {
		return cutUTF8(string(content), maxReadBytes) + fmt.Sprintf("\n\n...[内容过长，已经截断至前%d字节]...", maxReadBytes), nil
	}

	return string(content), nil
}

// resolve把路径钉死在工作区中，挡住../越界
func (t *ReadFileTool) resolve(rel string) (string, error) {
	full := filepath.Join(t.workDir, filepath.FromSlash(rel)) // filepath.FromSlash会把路径的斜杠转换成当前操作系统支持的分隔符，Join方法会自动凭借

	abs, err := filepath.Abs(full) // 挡住../越界，将拼接后的路径转换成标准的绝对路径，比如/app/a/../b会被转换成/app/b
	if err != nil {
		return "", fmt.Errorf("解析路径失败：%w", err)
	}

	// 将工作区根目录也转换成绝对目录，确保对比基准
	root, err := filepath.Abs(t.workDir)
	if err != nil {
		return "", fmt.Errorf("解析工作区失败：%w", err)
	}

	// 检查解析后的绝对路径是否满足：1. abs==root（刚好访问根目录本身），2. abs以root+"/"或者"\"开头
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径%q越出了工作区", rel)
	}

	// 校验通过，安全返回绝对路径
	return abs, nil
}

// cutUTF8按照字节截断，但不切断多字节字符
func cutUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
