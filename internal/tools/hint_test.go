package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"little-robot/internal/schema"
)

func TestHintFor(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		err      string
		wantHit  bool
		wantWord string
	}{
		{
			name:     "edit_file 多次命中",
			tool:     "edit_file",
			err:      "执行edit_file失败：old_text在文件中出现了3次，请补充相邻几行上下文以确保唯一",
			wantHit:  true,
			wantWord: "函数签名",
		},
		{
			name:     "edit_file 找不到",
			tool:     "edit_file",
			err:      "执行edit_file失败：在文件中未找到 old_text，请先 read_file 读取最新内容",
			wantHit:  true,
			wantWord: "read_file",
		},
		{
			name:     "read_file 路径不存在",
			tool:     "read_file",
			err:      "执行read_file失败：open nope.txt: no such file or directory",
			wantHit:  true,
			wantWord: "ls -la",
		},
		{
			name:     "bash 超时",
			tool:     "bash",
			err:      "输出xx\n[超时：命令执行超过30秒，已被终止]",
			wantHit:  true,
			wantWord: "nohup",
		},
		{
			name:     "bash 命令不存在",
			tool:     "bash",
			err:      "bash: line 1: rg: command not found",
			wantHit:  true,
			wantWord: "which",
		},
		{
			name:     "fetch_image 白名单",
			tool:     "fetch_image",
			err:      `执行fetch_image失败：域名 "evil.com" 不在白名单内`,
			wantHit:  true,
			wantWord: "Pexels",
		},
		{
			name:    "未命中不硬编提示",
			tool:    "bash",
			err:     "退出码非零：exit status 1\n输出something weird",
			wantHit: false,
		},
		{
			name:    "规则限定工具，不会串台",
			tool:    "read_file",
			err:     "执行read_file失败：command not found",
			wantHit: false, // command not found 只对 bash 生效
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hintFor(c.tool, c.err)
			if !c.wantHit {
				if got != "" {
					t.Fatalf("不应该命中，却返回了：%q", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("应该命中，但没有返回提示")
			}
			if !strings.Contains(got, c.wantWord) {
				t.Fatalf("提示里应包含 %q，实际：%q", c.wantWord, got)
			}
		})
	}
}

// 注册表层：错误结果里要带上救援指南，且原始报错不能被吞
func TestRegistryInjectsHint(t *testing.T) {
	reg := NewRegistry()
	reg.Register(failingTool{})

	res := reg.Execute(context.Background(), schema.ToolCall{
		ID:        "1",
		Name:      "explode",
		Arguments: json.RawMessage(`{}`),
	})

	if !res.IsError {
		t.Fatal("应该是错误结果")
	}
	if !strings.Contains(res.Output, "no such file or directory") {
		t.Fatalf("原始报错被吞了：%q", res.Output)
	}
	if !strings.Contains(res.Output, "【系统救援指南】") {
		t.Fatalf("没有注入救援指南：%q", res.Output)
	}
}

type failingTool struct{}

func (failingTool) Name() string { return "explode" }
func (failingTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "explode", Description: "总是失败，用于测试"}
}
func (failingTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("open x: %w", errors.New("no such file or directory"))
}
