package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"little-robot/internal/schema"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	_ BaseTool = &BashTool{}
)

type BashTool struct {
	workDir string
}

func NewBashTool(workDir string) *BashTool {
	return &BashTool{workDir: workDir}
}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "在工具区执行一条bash命令，支持&&串联，返回stdout和stderr的合并输出，最长8000字节，超时30秒，命令的厨师目录是工作区（Windows下用Git Bash，未安装则退回cmd）\n" +
			"注意：读取文件请用read_file，写入或整体覆盖文件请用write_file，修改已有文件中的局部内容请用edit_file，不要用cat/head读文件，不要用sed/echo写文件",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "要执行的bash命令，例如ls -la或者go build ./...",
				},
			},
			"required": []string{"command"},
		},
	}
}

type bashArgs struct {
	Command string `json:"command"`
}

func (t *BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in bashArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%w", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("command不能为空") // 这里不是工具问题是Agent的问题需要Agent处理
	}

	// 三十秒超时：防止模型写出死循环或者等输入的交互式命令，把整个引擎挂死
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// cmd := exec.CommandContext(timeoutCtx, "bash", "-c", in.Command)
	cmd := buildShellCommand(timeoutCtx, in.Command)
	cmd.Dir = t.workDir // 把当前cmd的执行滴滴放到workDir上面

	// CombinedOutput把stdout和stderr混在一起，因为编译错误全在stderr上面
	out, err := cmd.CombinedOutput()
	outputStr := string(out)

	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		return outputStr + "\n[超时：命令执行超过30秒，已被终止]", nil
	}

	if err != nil {
		return fmt.Sprintf("退出码非零：%v\n输出%s", err, outputStr), nil
	}
	if strings.TrimSpace(outputStr) == "" {
		return "命令执行成功，无输出", nil
	}

	const maxOutputBytes = 8000
	if len(outputStr) > maxOutputBytes {
		return cutUTF8(outputStr, maxOutputBytes) + fmt.Sprintf("\n\n...[输出过长，已截断至前%d字节]...", maxOutputBytes), nil
	}

	/*
		这里有个关键点需要理解：
		返回error：Registry把它翻成ToolResult{IsError: true, Output: "执行bash失败: ..."}
		返回nil：Registry原样把文本交给模型
	*/
	return outputStr, nil
}

// buildShellCommand按照平台挑选一个可用的shell
func buildShellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		if p := findGitBash(); p != "" {
			return exec.CommandContext(ctx, p, "-c", command)
		}
		return exec.CommandContext(ctx, "cmd", "/c", command)
	}
	return exec.CommandContext(ctx, "bash", "-c", command)
}

// findGitBash 只认Git for Windows的按爪给你路径
// 不能用exec.LookPath("bash")：Windows上面会命中C:\Windows\System32\bash.exe
// 那是WSL的转发器，没装发行版就会报错execvpe(/bin/bash) failed
func findGitBash() string {
	candidates := []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files\Git\usr\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Programs\Git\bin\bash.exe`),
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}
