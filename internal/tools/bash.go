package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"little-robot/internal/netutil"
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

// shellKind 记录这条命令最终会在哪种 shell 里执行。
// 它同时决定两件事：Description 教模型哪套语法、Execute 用哪个可执行文件。
// 两件事必须来自同一次判断——否则会出现“描述说 bash、实际跑 cmd”的错位，模型照着描述写反而更容易出错。
type shellKind int

const (
	shellGitBash shellKind = iota // Windows 且装了 Git for Windows
	shellCmd                      // Windows 没装 Git，退回 cmd.exe
	shellPosix                    // Linux/macOS 的 bash
)

type shellInfo struct {
	kind shellKind
	path string // 只有 Git Bash 用得到；其余为空，表示交给 PATH 里的 cmd / bash
}

type BashTool struct {
	workDir string
	shell   shellInfo
}

func NewBashTool(workDir string) *BashTool {
	// 构造时判断一次并记下来：Registry 每轮都会调 Definition()，每次重新探测既浪费，
	// 又可能让描述中途变脸，和实际执行用的 shell 对不上
	return &BashTool{
		workDir: workDir,
		shell:   detectShell(),
	}
}

func detectShell() shellInfo {
	if runtime.GOOS == "windows" {
		if p := findGitBash(); p != "" {
			return shellInfo{kind: shellGitBash, path: p}
		}
		return shellInfo{kind: shellCmd}
	}
	return shellInfo{kind: shellPosix}
}

func (t *BashTool) Name() string {
	return "bash"
}

// 不管哪种 shell 都一样的部分
const (
	bashPreamble = "在工作区执行一条命令，支持串联与重定向，返回 stdout 和 stderr 的合并输出，最长 8000 字节，超时 30 秒"
	bashFileRule = "注意：读取文件请用 read_file，写入或整体覆盖文件请用 write_file，修改已有文件中的局部内容请用 edit_file，不要用 cat/head 读文件，不要用 sed/echo 写文件"
)

// 方言提示：只说“Windows 下用 Git Bash”是不够的，模型会按 cmd 的习惯写 2>nul、dir /b、&，
// 而在 Git Bash 里 2>nul 不是丢弃输出，是在工作区建一个叫 nul 的文件。这里把差异写成能直接照抄的对照。
const gitBashRules = `当前 shell：Git Bash（POSIX sh），不是 cmd.exe：
- 丢弃输出写 >/dev/null 2>&1；不要写 2>nul 或 >nul——Git Bash 里 nul 只是个普通文件名，会在工作区生成垃圾文件
- 串联命令用 &&（前一条成功才继续）或 ;（无论成败都继续）；不要用 cmd 的 &，在 bash 里它表示把这个命令丢到后台
- 列目录用 ls / find，不要用 dir /b；路径分隔符用 /（Windows 的反斜杠在这里会被当成转义字符）
- 不要用 cmd 内建命令（dir/copy/del/type/set/where），改用 ls/cp/rm/cat/env/which`

const cmdRules = `当前 shell：cmd.exe（Windows 命令提示符），不是 bash：
- 丢弃输出写 >nul 2>&1
- 串联命令用 &（顺序执行）或 &&（前一条成功才继续）
- 列目录用 dir /b；路径分隔符用 Windows 的反斜杠（正斜杠多数命令也认）`

const posixRules = `当前 shell：bash（POSIX）：
- 丢弃输出写 >/dev/null 2>&1
- 串联命令用 && 或 ;`

func (t *BashTool) description() string {
	rules := posixRules
	switch t.shell.kind {
	case shellGitBash:
		rules = gitBashRules
	case shellCmd:
		rules = cmdRules
	}
	return bashPreamble + "。\n" + rules + "\n" + bashFileRule
}

func (t *BashTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: t.description(),
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "要执行的命令，例如ls -la或者go build ./...",
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

	cmd := t.command(timeoutCtx, in.Command)
	cmd.Dir = t.workDir // 把当前cmd的执行滴滴放到workDir上面

	// 把探测器找到的代理显式传给子进程：
	// netutil 的代理探测只作用于程序自己发的请求，bash 里的 curl/wget 完全不知道，
	// 不传就会走直连（有些网络下直接失败）。
	if proxy := netutil.Proxy(); proxy != nil {
		p := proxy.String()
		cmd.Env = append(os.Environ(),
			"HTTP_PROXY="+p, "http_proxy="+p,
			"HTTPS_PROXY="+p, "https_proxy="+p,
			"ALL_PROXY="+p, "all_proxy="+p,
		)
	}

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

// command 按构造时定下来的 shell 组装命令，跟 Description 里教的那套语法保持一致
func (t *BashTool) command(ctx context.Context, command string) *exec.Cmd {
	switch t.shell.kind {
	case shellGitBash:
		return exec.CommandContext(ctx, t.shell.path, "-c", command)
	case shellCmd:
		return exec.CommandContext(ctx, "cmd", "/c", command)
	default:
		return exec.CommandContext(ctx, "bash", "-c", command)
	}
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
