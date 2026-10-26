package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// 中文名要能解析成站点真实标签
func TestResolveAliases(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"爱弥斯":        "aemeath",
		"鸣潮爱弥斯":      "aemeath", // 包含匹配
		"爱弥斯 鸣潮 r18": "aemeath", // 噪音词要去掉
		"伊莉雅":        "illyasviel",
		"纳西妲":        "nahida",
		"AYSA":       "", // 错名字解析不出来（就该为空）
	}
	for in, want := range cases {
		got := resolveAliases(dir, in)
		if want == "" {
			if len(got) != 0 {
				t.Errorf("%q 不该解析出别名，实际 %v", in, got)
			}
			continue
		}
		if len(got) == 0 || got[0] != want {
			t.Errorf("%q 应该解析成 %q，实际 %v", in, want, got)
		}
	}
}

// 用户自定义别名文件要能覆盖/补充内置表
func TestResolveAliasesFromFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claw"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"塔菲": ["taffy"], "爱弥斯": ["aemeath_alt"]}`)
	if err := os.WriteFile(filepath.Join(dir, ".claw", "aliases.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	if got := resolveAliases(dir, "塔菲"); len(got) == 0 || got[0] != "taffy" {
		t.Errorf("自定义别名没生效：%v", got)
	}
	// 用户文件覆盖内置
	if got := resolveAliases(dir, "爱弥斯"); len(got) == 0 || got[0] != "aemeath_alt" {
		t.Errorf("用户别名应该覆盖内置：%v", got)
	}
}

// 单字别名不做"包含匹配"，避免误伤（比如"椿"出现在别的词里）
func TestResolveAliasesNoSingleCharContains(t *testing.T) {
	dir := t.TempDir()
	if got := resolveAliases(dir, "椿树下的本子"); len(got) != 0 {
		t.Errorf("单字别名不该做包含匹配，实际 %v", got)
	}
}
