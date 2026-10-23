package prompt

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// 固定人设与纪律 + AGENTS.md（项目规则） + .claw/skills/*/SKILL.md（场景技能）
// context是稀缺资源，但它必须“可编程”，硬编码的prompt改一次要重新编译，组装出来的prompt改一次只需要改Markdown

type Skill struct {
	Name        string
	Description string
	Body        string
}

// loadSkills扫描<workDir>/.claw/skills/*/SKILL.md，拼接成一段可以注入的文本
// 没有技能目录的时候返回空字符串（不报错）
func loadSkills(workDir string) string {
	base := filepath.Join(workDir, ".claw", "skills")
	if _, err := os.Stat(base); err != nil {
		return ""
	}

	var b strings.Builder
	count := 0

	// 这里的遍历会无视层级（深度），彻底扫描base目录下面的所有子目录和孙目录
	// filepath.WorkDir采用的是深度优先搜索（DFJS），在回调函数中虽然用了d.IsDir的nil返回，但是这本身不会组织WalkDir深入遍历内容，除非返回filepath.SkipDir
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		s := parseSkillMD(string(raw))
		b.WriteString(fmt.Sprintf("#### 技能：%s\n触发条件：%s\n\n执行指南：\n%s\n\n---\n", s.Name, s.Description, s.Body))
		count++
		return nil
	})

	if err != nil || count == 0 {
		return ""
	}

	return "\n# 可用技能（符合触发条件时必须遵守）\n" + b.String()
}

// parseSkillMD 解析极简frontmatter：--- 之间的name/description，其余是正文
func parseSkillMD(content string) Skill {
	s := Skill{
		Name:        "未命名技能",
		Description: "未命名触发条件",
		Body:        content,
	}

	normalized := strings.ReplaceAll(content, "\r\n", "\n") // 在Windows上面写SKILL.md大概率是CRLF
	if !strings.HasPrefix(normalized, "---\n") {
		return s
	}

	parts := strings.SplitN(normalized, "---\n", 3) // 分割成三个部分
	if len(parts) < 3 {
		return s
	}

	s.Body = strings.TrimSpace(parts[2])
	for _, line := range strings.Split(parts[1], "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "name:"):
			s.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		case strings.HasPrefix(line, "description:"):
			s.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}

	return s
}
