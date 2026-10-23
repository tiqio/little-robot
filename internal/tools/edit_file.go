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
	_ BaseTool = &EditFileTool{}
)

type EditFileTool struct {
	workDir string
}

func NewEditFileTool(workDir string) *EditFileTool {
	return &EditFileTool{workDir: workDir}
}

func (t *EditFileTool) Name() string {
	return "edit_file"
}

func (t *EditFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "对现有文件做局部字符串替换，比重写整个文件更加安全，old_text必须在文件中唯一出现，不确定时先read_file，匹配失败会报错，请补充更多上下文重试",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要修改的文件路径（相对工作区）",
				},
				"old_text": map[string]interface{}{
					"type":        "string",
					"description": "文件中的原有文本，必须包含足够上下文以保持唯一",
				},
				"new_text": map[string]interface{}{
					"type":        "string",
					"description": "替换后的新文本",
				},
			},
			"required": []string{"path", "old_text", "new_text"},
		},
	}
}

type editFileArgs struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

func (t *EditFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in editFileArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%w", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path不能为空")
	}
	if in.OldText == "" {
		return "", fmt.Errorf("old_text不能为空")
	}

	full, err := resolvePath(t.workDir, in.Path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(full)
	if err != nil {
		return "", fmt.Errorf("读取%q失败，请确认文件存在：%w", in.Path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q是目录，不是文件", in.Path)
	}

	// 把目标文件读取出来，修改完了再放回去
	original, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("读取%q失败：%w", in.Path, err)
	}

	newContent, err := replaceOnce(string(original), in.OldText, in.NewText)
	if err != nil {
		return "", err
	}

	// 用原文件的权限协会，可以防止把可执行脚本降级成0644
	if err := os.WriteFile(full, []byte(newContent), info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("写回%q失败：%w", in.Path, err)
	}

	// 允许new_text为空（删代码是合法需求），但返回值必须说清楚
	if in.NewText == "" {
		return fmt.Sprintf("已修改%s：删除1处（新文本为空），文件从%d字节变为%d字节", in.Path, len(original), len(newContent)), nil
	}

	return fmt.Sprintf("已修改%s：替换1处，文件从%d字节，修改为%d字节", in.Path, len(original), len(newContent)), nil
}

func replaceOnce(original, oldText, newText string) (string, error) {
	// 第一级：精确匹配
	count := strings.Count(original, oldText)
	switch {
	case count == 1:
		return strings.Replace(original, oldText, newText, 1), nil
	case count > 1:
		return "", fmt.Errorf("old_text在文件中出现了%d次，请补充相邻几行上下文以确保唯一", count)
	}

	// 第二级：按行匹配，忽略每行首尾空白（模型最常见的错误是缩进对不上）
	origLines := strings.Split(original, "\n")
	oldLines := strings.Split(strings.Trim(oldText, "\n"), "\n")
	if len(oldLines) > len(origLines) {
		return "", fmt.Errorf("在文件中未找到 old_text，请先 read_file 读取最新内容")
	}

	var hits []int
	for i := 0; i+len(oldLines) <= len(origLines); i++ {
		match := true
		for j := range oldLines {
			if strings.TrimSpace(origLines[i+j]) != strings.TrimSpace(oldLines[j]) {
				match = false
				break
			}
		}
		if match {
			hits = append(hits, i)
		}
	}

	switch {
	case len(hits) == 0:
		return "", fmt.Errorf("在文件中未找到 old_text（已尝试忽略缩进），请先 read_file 读取最新内容再试")
	case len(hits) > 1:
		return "", fmt.Errorf("old_text 模糊匹配到了 %d 处，请补充上下文以精确定位", len(hits))
	}

	i := hits[0]
	merged := make([]string, 0, len(origLines))
	merged = append(merged, origLines[:i]...)
	merged = append(merged, strings.Split(strings.Trim(newText, "\n"), "\n")...)
	merged = append(merged, origLines[i+len(oldLines):]...)
	return strings.Join(merged, "\n"), nil
}
