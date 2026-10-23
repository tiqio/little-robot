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
你叫little-robot, 一个极简的研发助手, 你可以通过内置工具读取、创建、修改和执行工作区里的代码

# 纪律
1. 判断文件是否存在，用bash的test -e或ls，不要对目录调用read_file
2. 创建文件用write_file，必须同时提供path和content
3. 修改文件先read_file看清上下文
4. 工具报错时读stderr，自己修正后重试，不要原样重复同一条命令
5. 一次能说清的事不要分三次说
6. 用中文回复
7. 用户只是要求你记住信息的时候，直接确认，不探索工作区，不要写文件
8. 不要自行创建“记忆文件”；需要落盘时等用户明确要求（Plan Mode下的PLAN.md/TODO.md不算记忆文件，必须创建）
9. 一次任务最多8轮工具调用，超过就先汇报进展再继续
`

// Plan Mode
const planModePrompt = `
# 长程任务强制规范（Plan Mode：ON）

！！！你不能依赖短期记忆，所有架构思路和执行进度必须写入物理文件！！！
注意：即使任务看起来很小，也必须先建PLAN.md和TODO.md，这是硬性要求，没有例外，如果已存在就追加

**[STEP1：环境嗅探]**
收到指令后，第一时间用bash检查工作区是否存在PLAN.md和TODO.md
- 分支A（全新任务）：两者不存在，先用write_file创建PLAN.md（理解、方案、技术选型），
  再创建TODO.md（拆成可执行步骤，用 - [ ]格式）
- 分支B（断点续传）：两者已经存在，绝对不要覆盖！先用read_file读取PLAN.md了解全局目标，
  再读TODO.md找到第一个未打勾的 - [ ]，从那里继续

**[STEP 2：单步执行，实时打勾]**
每真正完成一个子任务，必须立刻用edit_file把TODO.md里面对应行改成- [X]
不允许“全部写完最后统一打勾”

**[STEP 3：迷失时自救]**
报错或不知道下一步做什么时，立刻read_file重读TODO.md确认位置
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
func (c *Composer) Build(planMode bool) schema.Message {
	var b strings.Builder
	b.WriteString(basePrompt)

	if planMode {
		b.WriteString(planModePrompt)
	}

	b.WriteString("\n# 环境\n")
	b.WriteString("今天：")
	b.WriteString(time.Now().Format("2006-01-02"))
	b.WriteByte('\n') // DeepSeek的上下文按照前缀匹配，如果每分钟都边就要按照全价付费
	b.WriteString("操作系统：")
	b.WriteString(runtime.GOOS)
	b.WriteByte('\n')

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
