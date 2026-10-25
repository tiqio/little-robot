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
	_ BaseTool = &WriteFileTool{}
)

type WriteFileTool struct {
	workDir string
}

func NewWriteFileTool(workDir string) *WriteFileTool {
	return &WriteFileTool{workDir: workDir}
}

func (t *WriteFileTool) Name() string {
	return "write_file"
}

func (t *WriteFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "创建或者整体覆盖写入一个文件，父目录不存在的时候会自动创建，路径必须是工作区的相对路径，注意：这是整体覆盖不是局部修改",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "相对工作区的文件路径，例如notes/demo.md",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "要写入的完整文件内容",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *WriteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in writeFileArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%w", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path不能为空")
	}

	dir := effectiveWorkDir(ctx, t.workDir)
	full, err := resolvePath(dir, in.Path) // 这里指定的path不能超出这个工作目录的范围
	if err != nil {
		return "", err
	}

	// 父母路可能还不存在，先补上
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("创建目录%q失败：%w", filepath.Dir(full), err)
	}

	if err := os.WriteFile(full, []byte(in.Content), 0o644); err != nil {
		return "", fmt.Errorf("写入%q失败：%w", in.Path, err)
	}

	return fmt.Sprintf("已经写入%s（%d字节）", in.Path, len(in.Content)), nil
}
