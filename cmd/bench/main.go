package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"little-robot/internal/config"
	"little-robot/internal/eval"
)

func main() {
	_ = config.LoadEnvFile(".env")

	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		log.Fatal("DEEPSEEK_API_KEY没有设置，检查.env")
	}

	model := config.Get("DEEPSEEK_MODEL", "deepseek-flash")
	runner := &eval.Runner{
		APIKey:  apiKey,
		BaseURL: config.Get("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		Model:   model,
		OutRoot: filepath.Join(".claw", "bench"),
	}

	cases := []eval.TestCase{
		{
			ID:             "001_write_file",
			Name:           "新建文件并写入内容",
			TaskPrompt:     `在当前目录创建 plan.md，写入三行内容，第一行是"# 本周计划"。写完用 bash 确认文件存在。`,
			Timeout:        2 * time.Minute,
			ValidateScript: `test -s plan.md && head -1 plan.md | grep -q "本周计划"`,
		},
		{
			ID:   "002_fix_bug",
			Name: "跑测试并修复 bug",
			SetupScript: `
cat > go.mod <<'EOF'
module demo

go 1.21
EOF
cat > math.go <<'EOF'
package demo

func Add(a, b int) int {
	return a - b
}
EOF
cat > math_test.go <<'EOF'
package demo

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatalf("Add(2,3) = %d, want 5", Add(2, 3))
	}
}
EOF
`,
			TaskPrompt:     "当前目录有一个 Go 模块，go test 是失败的。请找出原因并修复，修好后跑 go test 确认通过。",
			Timeout:        4 * time.Minute,
			ValidateScript: `go test ./... >/dev/null 2>&1`,
		},
		{
			ID:   "003_search_answer",
			Name: "跨文件搜索并落盘结论",
			SetupScript: `
mkdir -p docs
echo "普通内容" > docs/a.md
echo "这里藏着 MAGIC_TOKEN_42 这个标记" > docs/b.md
echo "普通内容" > docs/c.md
`,
			TaskPrompt:     "在 docs 目录里找出包含 MAGIC_TOKEN_42 的文件，把它的文件名（只有文件名，不带路径）写进 answer.txt。",
			Timeout:        2 * time.Minute,
			ValidateScript: `test -f answer.txt && grep -q "b.md" answer.txt`,
		},
	}

	results := runner.Run(context.Background(), cases)

	// 结果表：屏幕上是给你扫的，明细在 results-*.json 里
	// 注意：%-24s 是按“字符数”补空格，而中文在终端里占两列，所以必须自己按显示宽度对齐
	fmt.Println()
	fmt.Printf("%s %s %s %s %s %s %s\n",
		padRight("ID", 18), padRight("名称", 26), padRight("结果", 6),
		padLeft("耗时", 8), padLeft("花费(元)", 10), padLeft("LLM", 5), padLeft("工具", 5))
	fmt.Println(strings.Repeat("-", 92))
	passed, totalCost := 0, 0.0
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		totalCost += r.CostCNY
		if r.Passed {
			passed++
		}
		fmt.Printf("%s %s %s %s %s %s %s\n",
			padRight(r.ID, 18), padRight(r.Name, 26), padRight(status, 6),
			padLeft(fmt.Sprintf("%dms", r.DurationMs), 8),
			padLeft(fmt.Sprintf("%.6f", r.CostCNY), 10),
			padLeft(strconv.Itoa(r.LLMCalls), 5),
			padLeft(strconv.Itoa(r.ToolCalls), 5))
		if !r.Passed {
			if r.Err != "" {
				fmt.Printf("    错误：%s\n", r.Err)
			}
			if r.ValidateOut != "" {
				fmt.Printf("    校验输出：%s\n", r.ValidateOut)
			}
		}
	}
	fmt.Println(strings.Repeat("-", 92))
	fmt.Printf("通过 %d/%d ｜ 总花费 %.6f 元\n\n", passed, len(results), totalCost)

	// 落盘，便于改 prompt / 换模型之后做对比
	outDir := filepath.Join(".claw", "bench")
	_ = os.MkdirAll(outDir, 0o755)
	outPath := filepath.Join(outDir, fmt.Sprintf("results-%s.json", time.Now().Format("20060102-150405")))
	b, _ := json.MarshalIndent(map[string]any{
		"model":   model,
		"passed":  passed,
		"total":   len(results),
		"cost":    totalCost,
		"results": results,
	}, "", "  ")
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		log.Printf("写结果文件失败：%v", err)
	} else {
		fmt.Printf("结果已保存：%s\n", outPath)
	}
}

// displayWidth 算字符串在终端里占几列：中文/全角字符占 2 列
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if isWide(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // 韩文字母
		r >= 0x2E80 && r <= 0xA4CF, // CJK 部首/汉字/假名等
		r >= 0xAC00 && r <= 0xD7A3, // 韩文音节
		r >= 0xF900 && r <= 0xFAFF, // CJK 兼容汉字
		r >= 0xFE30 && r <= 0xFE6F, // CJK 兼容形式
		r >= 0xFF00 && r <= 0xFF60, // 全角 ASCII
		r >= 0xFFE0 && r <= 0xFFE6: // 全角符号
		return true
	}
	return false
}

// padRight 左对齐补空格
func padRight(s string, width int) string {
	if n := width - displayWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// padLeft 右对齐补空格（数字列用）
func padLeft(s string, width int) string {
	if n := width - displayWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}
