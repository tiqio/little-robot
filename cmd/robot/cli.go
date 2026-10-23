package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"little-robot/internal/version"
)

// handleArgs 解析命令行参数。返回 handled=true 表示本次调用只需要打印信息并退出，
// 调用方不应再继续装配引擎。
func handleArgs(args []string, stdout, stderr io.Writer) (handled bool, err error) {
	fs := flag.NewFlagSet("robot", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var showVersion bool
	fs.BoolVar(&showVersion, "version", false, "打印版本号后退出")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "用法：robot [选项]\n\n选项：\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil // -h：用法已由 fs.Usage 打印，正常退出
		}
		return true, fmt.Errorf("解析命令行参数: %w", err)
	}

	if showVersion {
		fmt.Fprintf(stdout, "little-robot %s\n", version.String())
		return true, nil
	}

	if fs.NArg() > 0 {
		fs.Usage()
		return true, fmt.Errorf("未知参数: %v", fs.Args())
	}

	return false, nil
}
