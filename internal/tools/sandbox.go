package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolve把路径钉死在工作区中，挡住../越界
func resolvePath(workDir string, rel string) (string, error) {
	full := filepath.Join(workDir, filepath.FromSlash(rel)) // filepath.FromSlash会把路径的斜杠转换成当前操作系统支持的分隔符，Join方法会自动凭借

	abs, err := filepath.Abs(full) // 挡住../越界，将拼接后的路径转换成标准的绝对路径，比如/app/a/../b会被转换成/app/b
	if err != nil {
		return "", fmt.Errorf("解析路径失败：%w", err)
	}

	// 将工作区根目录也转换成绝对目录，确保对比基准
	root, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("解析工作区失败：%w", err)
	}

	// 检查解析后的绝对路径是否满足：1. abs==root（刚好访问根目录本身），2. abs以root+"/"或者"\"开头
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径%q越出了工作区", rel)
	}

	// 校验通过，安全返回绝对路径
	return abs, nil
}
