package prompt

import (
	"little-robot/internal/schema"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// 系统提示词是整个项目里投入产出比最高的一段字符串
const basePrompt = `# 身份
你叫little-robot，一个极简的研发助手，你可以通过内置工具读取、创建、修改和执行工作区里的代码

# 纪律
1. 判断文件是否存在，用run_command的dir或type，不要对目录调用read_file
2. 创建文件用write_file，必须同时提供path和content
3. 修改文件先read_file看清上下文
4. 工具报错时读stderr，自己修正后重试，不要原样重复同一条命令
5. 一次能说清的事不要分三次说
6. 用中文回复
7. 用户只是要求你记住信息的时候，直接确认，不探索工作区，不要写文件
8. 不要自行创建“记忆文件”；需要落盘时等用户明确要求
9. 一次任务最多8轮工具调用，超过就先汇报进展再继续
`

type Composer struct {
	workDir string
}

func NewComposer(workDir string) *Composer {
	return &Composer{
		workDir: workDir,
	}
}

// Build组装system消息：固定人设+AGENTS.md+Skills
func (c *Composer) Build() schema.Message {
	var b strings.Builder
	b.WriteString(basePrompt)

	b.WriteString("\n# 环境\n")
	b.WriteString("今天：" + time.Now().Format("2006-01-02") + "\n") // DeepSeek的上下文按照前缀匹配，如果每分钟都边就要按照全价付费
	b.WriteString("操作系统：" + runtime.GOOS + "\n")

	if raw, err := os.ReadFile(filepath.Join(c.workDir, "AGENTS.md")); err == nil {
		b.WriteString("\n# 项目专属指南（来自AGENTS.md，必须遵守）\n```markdown\n")
		b.WriteString(string(raw))
		b.WriteString("\n```\n") // 还转了一下markdown格式
	}

	if skills := loadSkills(c.workDir); skills != "" {
		b.WriteString(skills)
	}

	return schema.Message{
		Role:    schema.RoleSystem,
		Content: b.String(),
	}
}
