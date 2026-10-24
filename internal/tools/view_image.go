package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"little-robot/internal/schema"
)

// ViewImageTool 让模型"看"工作区里已有的图片。
// 典型用途：fetch_image 下载后不确定，再单独看一眼；或者用户给的截图。
var _ BaseTool = &ViewImageTool{}

type ViewImageTool struct {
	workDir string
}

func NewViewImageTool(workDir string) *ViewImageTool { return &ViewImageTool{workDir: workDir} }

func (t *ViewImageTool) Name() string { return "view_image" }

func (t *ViewImageTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "查看工作区里的一张图片（png/jpg/webp/gif），图片会作为消息回传给你。" +
			"用于确认图片内容是否符合要求，或查看用户提供的截图。不要用它发送图片（发送用 send_image）。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "相对工作区的图片路径",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (t *ViewImageTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%w", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path 不能为空")
	}

	full, err := resolvePath(t.workDir, in.Path)
	if err != nil {
		return "", err
	}

	dataURL, err := fileToDataURL(full)
	if err != nil {
		return "", err
	}

	if box, ok := ctx.Value(imageCarrierKey{}).(*[]string); ok && box != nil {
		*box = append(*box, dataURL)
	}
	return fmt.Sprintf("已加载图片 %s，请查看。", in.Path), nil
}
