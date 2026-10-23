package guard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"little-robot/internal/schema"
	"little-robot/internal/tools"
	"log"
	"os"
	"regexp"
	"strings"
)

// 策略：什么算危险
type rule struct {
	re  *regexp.Regexp
	why string
}

var rules = []rule{
	{regexp.MustCompile(`(?i)\brm\s+-[a-z]*[rf]`), "递归/强制删除"},
	{regexp.MustCompile(`(?i)\b(del|erase)\s+/[fsq]`), "Windows 强制删除"},
	{regexp.MustCompile(`(?i)\brmdir\s+/s`), "递归删除目录"},
	{regexp.MustCompile(`(?i)remove-item[^\n]*-recurse`), "PowerShell 递归删除"},
	{regexp.MustCompile(`(?i)\b(sudo|runas)\b`), "提权执行"},
	{regexp.MustCompile(`(?i)git\s+(reset\s+--hard|clean\s+-[a-z]*f|push[^\n]*--force)`), "不可逆的 git 操作"},
	{regexp.MustCompile(`(?i)\b(shutdown|reboot)\b`), "关机/重启"},
	{regexp.MustCompile(`(?:^|\s)>>?\s*[\w.\\/-]`), "重定向覆盖文件"},
}

// Check 判断这次调用是否危险，返回原因（给人和模型看）
func Check(call schema.ToolCall) (bool, string) {
	if call.Name != "bash" && call.Name != "run_command" {
		return false, ""
	}

	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return false, ""
	}
	for _, r := range rules {
		if r.re.MatchString(args.Command) {
			return true, r.why + ":" + args.Command
		}
	}
	return false, ""
}

type Approver interface {
	Ask(ctx context.Context, toolName, args, reason string) (bool, error)
}

// 审批人：在终端问y/N
type TerminalApprover struct {
	in  *bufio.Reader
	out io.Writer
}

func NewTerminalApprover() *TerminalApprover {
	return &TerminalApprover{
		in:  bufio.NewReader(os.Stdin),
		out: os.Stderr,
	}
}

func (a *TerminalApprover) Ask(ctx context.Context, toolName, args, reason string) (bool, error) {
	fmt.Fprintf(a.out, "\n[高危操作审批]\n\t工具：%s\n\t原因：%s\n\t参数：%s\n\t允许执行吗？[y/N] ", toolName, reason, args)

	type res struct {
		line string
		err  error
	}
	done := make(chan res, 1)
	go func() {
		line, err := a.in.ReadString('\n')
		done <- res{line, err}
	}()

	select {
	case <-ctx.Done():
		return false, ctx.Err() // 任务被取消的时候就不要挂着
	case r := <-done:
		if r.err != nil {
			return false, r.err
		}
		s := strings.ToLower(strings.TrimSpace(r.line))
		return s == "y" || s == "yes", nil
	}
}

// AutoApprover 用于CI/无人值守，直接放行
type AutoApprover struct{}

func (AutoApprover) Ask(ctx context.Context, _, _, _ string) (bool, error) {
	return true, nil
}

// 中间件：查询安全与否然后利用Ask进行审批
func Middleware(a Approver) tools.MiddlewareFunc {
	return func(ctx context.Context, call schema.ToolCall) (bool, string) {
		danger, why := Check(call)
		if !danger {
			return true, ""
		}

		ok, err := a.Ask(ctx, call.Name, string(call.Arguments), why)
		if err != nil {
			return false, "审批过程出错（" + err.Error() + "），按拒绝处理"
		}
		if !ok {
			return false, "用户拒绝了这个操作：" + why
		}

		log.Printf("[Guard] 用户批准：%s", call.Name)
		return true, ""
	}
}
