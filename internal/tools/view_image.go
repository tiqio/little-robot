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
		Description: "查看工作区里的一张本地图片（jpg/png/webp/gif）：图片会回传给你，但**用户看不到**——只进你的眼睛。" +
			"用于确认「这张图对不对」，或看用户发来的截图。要交付给用户请用 send。" +
			"fetch_image 下载时图已经回传过，一般不用再看一次。",
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

	dir := effectiveWorkDir(ctx, t.workDir)
	full, err := resolvePath(dir, in.Path)
	if err != nil {
		return "", err
	}

	// fileToDataURL 先做一次格式校验（不是图片要报清楚），再复用算好的 dataURL 挂 carrier
	dataURL, err := fileToDataURL(full)
	if err != nil {
		return "", err
	}
	attachDataURL(ctx, dataURL)
	return fmt.Sprintf("已加载图片 %s，请查看。", in.Path), nil
}
