package runstate

import (
	"context"
)

type RunState struct {
	WorkDir  string // 本次运行的目录覆盖；空=用引擎默认的
	PlanPath string // 已定稿方案（后面做审批用）
	PlanHash string
	Approved bool
}

type ctxKey struct{}

// New 通常由engine.Run在开头调用一次
func New() *RunState {
	return &RunState{}
}

//func WithRunState(ctx context.Context, rs *RunState) context.Context
//func RunStateFrom(ctx context.Context) *RunState

func With(ctx context.Context, s *RunState) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// From 取不到的时候返回nil，调用方必须判空
func From(ctx context.Context) *RunState {
	s, _ := ctx.Value(ctxKey{}).(*RunState)
	return s
}
