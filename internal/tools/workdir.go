package tools

import (
	"context"
	"little-robot/internal/runstate"
)

// effectiveWorkDir 决定这次工具调用落在哪个目录
// 运行态有覆盖就用它（worktree），否则用工具构造时的默认目录
func effectiveWorkDir(ctx context.Context, fallback string) string {
	if rs := runstate.From(ctx); rs != nil && rs.WorkDir != "" {
		return rs.WorkDir
	}
	return fallback
}
