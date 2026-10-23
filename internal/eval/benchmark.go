package eval

import (
	"context"
	"fmt"
	"little-robot/internal/engine"
	"little-robot/internal/observability"
	"little-robot/internal/provider"
	"little-robot/internal/schema"
	"little-robot/internal/session"
	"little-robot/internal/tools"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Package eval是一个极简的Agent评测框架：一次评测 = 靶机（干净工作区）+任务（prompt）+校验脚本（exit code）
// 原则：让机器判定成败，不要让模型自评

// TestCase 一个评测用例
type TestCase struct {
	ID             string        // 用例标识，同时用作工作区目录名
	Name           string        // 人类可读的名字
	SetupScript    string        // 可选：在干净工作区里先跑一遍，搭靶机
	TaskPrompt     string        // 发给Agent的任务
	ValidateScript string        // 校验脚本，exit 0视为通过，校验用脚本，不用Go函数，因为脚本是语言无关的
	Timeout        time.Duration // 单个用例总超时，0用默认值
}

// TestResult 一次评测结果
type TestResult struct {
	ID           string
	Name         string
	Passed       bool
	DurationMs   int64
	CostCNY      float64
	LLMCalls     int
	ToolCalls    int
	HitTokens    int
	MissTokens   int
	OutputTokens int
	Err          string
	ValidateOut  string
	WorkDir      string
}

// Runner 负责跑一批用例
type Runner struct {
	APIKey      string
	BaseURL     string
	Model       string
	OutRoot     string // 每个用例的工作区根目录
	EnableThink bool
}

const defaultTimeout = 3 * time.Minute

// runBash 在指定目录执行脚本，返回合并输出：exit非零时返回error
func runBash(ctx context.Context, dir, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// truncate 按照rune截断，避免把中文劈坏
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// nopReporter 让评测时终端安静
type nopReporter struct{}

func (n *nopReporter) OnThinkingStart(ctx context.Context)                  {}
func (n *nopReporter) OnThinkingText(ctx context.Context, text string)      {}
func (n *nopReporter) OnAssistantMessage(ctx context.Context, text string)  {}
func (n *nopReporter) OnToolCall(ctx context.Context, call schema.ToolCall) {}
func (n *nopReporter) OnToolResult(ctx context.Context, call schema.ToolCall, res schema.ToolResult) {
}

// Run 依次跑完所有用例
// 串行不是并发：并发跑会让成败和延迟相互污染（限流、CPU抢占），数据就没办法比对了
func (r *Runner) Run(ctx context.Context, cases []TestCase) []TestResult {
	if r.OutRoot == "" {
		r.OutRoot = filepath.Join(".claw", "bench")
	}

	results := make([]TestResult, 0, len(cases))
	for _, tc := range cases {
		results = append(results, r.runCase(ctx, tc))
	}
	return results
}

func (r *Runner) runCase(ctx context.Context, tc TestCase) TestResult {
	res := TestResult{ID: tc.ID, Name: tc.Name}
	start := time.Now()

	// 1. 每个用例用全新的工作区：可重复、互不污染
	workDir := filepath.Join(r.OutRoot, tc.ID)
	res.WorkDir = workDir

	// 为什么要RemoveAll再MkdirAll：第二次跑同一个用例的时候，上次留下的plan.md会让test -s plan.md直接通过，就是说环境没有清理干净
	if err := os.RemoveAll(workDir); err != nil {
		res.Err = fmt.Sprintf("清理工作区失败：%v", err)
		return res
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		res.Err = fmt.Sprintf("创建工作区失败：%v", err)
		return res
	}

	// 2. 搭建靶机
	if tc.SetupScript != "" {
		if out, err := runBash(ctx, workDir, tc.SetupScript); err != nil {
			res.Err = fmt.Sprintf("靶机初始化失败：%v\n%s", err, truncate(out, 300))
			return res
		}
	}

	// 3. 每个用例一套独立的provider/tracker，成本单独统计
	p := provider.NewDeepSeekProvider(r.APIKey, r.BaseURL, r.Model)
	tracker := observability.NewCostTracker(p, r.Model, observability.PriceForNow(time.Now()))

	reg := tools.NewRegistry()
	reg.Register(tools.NewReadFileTool(workDir))
	reg.Register(tools.NewWriteFileTool(workDir))
	reg.Register(tools.NewEditFileTool(workDir))
	reg.Register(tools.NewBashTool(workDir))
	// 故意不挂 guard：人工审批要读 stdin，无人值守跑评测会永远卡住，所以cmd/bench要单独写一份registry，而不是复用cmd/robot的

	eng := engine.NewAgentEngine(tracker, reg, workDir, r.EnableThink)
	sess := session.New(tc.ID, workDir)

	// 4. 跑Agent
	timeout := tc.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	caseCtx, cancel := context.WithTimeout(ctx, timeout)
	runErr := eng.Run(caseCtx, sess, tc.TaskPrompt, &nopReporter{})
	cancel()

	st := tracker.Stats()
	res.CostCNY = st.CostCNY
	res.LLMCalls = st.Calls
	res.HitTokens = st.HitTokens
	res.MissTokens = st.MissTokens
	res.OutputTokens = st.OutputTokens
	res.ToolCalls = countToolCalls(sess.Messages())

	if runErr != nil {
		res.Err = runErr.Error()
	}

	// 5. 校验：用独立的ctx，别让Agent超时连带把校验也取消掉
	valCtx, valCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	out, valErr := runBash(valCtx, workDir, tc.ValidateScript)
	valCancel()
	res.ValidateOut = truncate(out, 300)

	res.Passed = runErr == nil && valErr == nil
	res.DurationMs = time.Since(start).Milliseconds()
	return res
}

func countToolCalls(msgs []schema.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.ToolCalls)
	}
	return n
}
