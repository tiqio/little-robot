package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"little-robot/internal/version"
)

// options 是命令行解析出来的运行参数
type options struct {
	Prompt    string // 要执行的任务
	Dir       string // 工作区
	SessionID string // 会话ID，同名会接着上次的历史
	Think     bool   // 两阶段慢思考
	Plan      bool   // 计划模式（维护PLAN.md/TODO.md）
}

// handleArgs 解析命令行参数。返回 handled=true 表示本次调用只需要打印信息并退出，
// 调用方不应再继续装配引擎。
func handleArgs(args []string, stdout, stderr io.Writer) (opts options, handled bool, err error) {
	fs := flag.NewFlagSet("robot", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var showVersion bool
	fs.StringVar(&opts.Prompt, "p", "", "要执行的任务，例如-p 修一下docs里的错别字")
	fs.StringVar(&opts.Dir, "dir", ".", "工作区目录")
	fs.StringVar(&opts.SessionID, "session", "local", "会话ID，同名会话接着上次的历史继续")
	fs.BoolVar(&opts.Think, "think", false, "开启两阶段慢思考（先规划再动手）")
	fs.BoolVar(&opts.Plan, "plan", true, "开启计划模式，让Agent维护PLAN.md/TODO.md（-plan=false关闭）")
	fs.BoolVar(&showVersion, "version", false, "打印版本号后退出")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "用法：robot -p \"任务描述\" [选项]\n\n选项：\n")
		fs.PrintDefaults()
		fmt.Fprintf(stderr, "\n例子：\n")
		fmt.Fprintf(stderr, "  robot -p \"把 docs 里的错别字修一下\" -dir ./docs\n")
		fmt.Fprintf(stderr, "  robot -p \"继续上一轮没做完的事\" -session fix-typo\n")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, true, nil // -h：用法已由 fs.Usage 打印，正常退出
		}
		return opts, true, fmt.Errorf("解析命令行参数: %w", err)
	}

	if showVersion {
		fmt.Fprintf(stdout, "little-robot %s\n", version.String())
		return opts, true, nil
	}

	if fs.NArg() > 0 {
		fs.Usage()
		return opts, true, fmt.Errorf("未知参数: %v", fs.Args())
	}

	return opts, false, nil
}
