package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"little-robot/internal/schema"
	"os"
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

	// 运行时设置这个工作目录
	dir := effectiveWorkDir(ctx, t.workDir)
	full, err := resolvePath(dir, in.Path)
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
