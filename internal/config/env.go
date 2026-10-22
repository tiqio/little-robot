package config

import (
	"bufio"
	"log"
	"os"
	"strings"
)

// LoadEnvFile读取KEY=VALUE形式的文件，写入环境变量
// 已经存在的环境变量不覆盖
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`) // 指定了字符集，用以去除双引号和单引号
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			log.Printf("当前存在环境变量：%s\n", key)
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		} else {
			// log.Printf("导入环境变量：%s\n", key)
		}
	}
	return scanner.Err()
}

func Get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
