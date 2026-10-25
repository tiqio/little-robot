package config

import (
	"bufio"
	"log"
	"os"
	"strconv"
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

// Int 读取 int 型环境变量；解析失败或 <=0 时回落到 def，不炸启动。
// 思考/行动预算这类"给了就用、没给就默认"的数值配置走这里。
func Int(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n > 0 {
			return n
		}
	}
	return def
}

// Bool 读取布尔型环境变量：true/1/yes/on 为真，其余为假（含空串）。
func Bool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}
