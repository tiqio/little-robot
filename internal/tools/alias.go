package tools

import (
	"encoding/json"
	"os"
	"strings"
)

// 本地别名表：中文/日文音译 → nhentai 上真实存在的英文标签。
//
// 为什么需要它：模型把「爱弥斯」猜成 "aysa"（正确是 aemeath），
// 于是无论怎么重试都是 0 条。这不是换数据源能解决的——数据源没错，
// 缺的是"把用户说法翻译成站点标签"这一层，而模型不掌握这张对照表。
//
// 两道来源：
//  1. 这里的内置表（覆盖常见角色，改代码就能加）；
//  2. <工作区>/.claw/aliases.json（用户自己补，不用改代码）。
//
// 键用小写、不含空格/下划线，匹配时两边都做同样归一化。
var builtinAliases = map[string][]string{
	// 鸣潮 Wuthering Waves
	"爱弥斯": {"aemeath"}, "エイメス": {"aemeath"},
	"长离": {"changli"}, "今汐": {"jinhsi"}, "守岸人": {"shorekeeper"},
	"菲比": {"phoebe"}, "卡提希娅": {"cartethyia"}, "椿": {"camellya"},
	"散华": {"sanhua"}, "吟霖": {"yinlin"}, "忌炎": {"jiyan"},
	// 原神 Genshin Impact
	"纳西妲": {"nahida"}, "伊莉雅": {"illyasviel"}, "雷电将军": {"raiden_shogun"},
	"芙宁娜": {"furina"}, "胡桃": {"hu_tao"}, "甘雨": {"ganyu"},
	"八重神子": {"yae_miko"}, "神里绫华": {"kamisato_ayaka"}, "可莉": {"klee"},
	"宵宫": {"yoimiya"}, "妮露": {"nilou"}, "克洛琳德": {"clorinde"},
	// 其他常见
	"明日香": {"souryuu_asuka_langley"}, "绫波丽": {"ayanami_rei"},
	"初音": {"hatsune_miku"}, "雷姆": {"rem_(re:zero)"}, "拉姆": {"ram_(re:zero)"},
}

// aliasFile 用户自定义别名文件（相对工作区）
const aliasFile = ".claw/aliases.json"

// resolveAliases 把用户/模型给的说法解析成候选英文标签。
// 命中规则：整串匹配，或输入里包含某个别名键（"鸣潮爱弥斯" 也能命中 爱弥斯）。
func resolveAliases(workDir, query string) []string {
	table := loadAliases(workDir)
	q := normalizeAlias(query)
	if q == "" {
		return nil
	}

	// 1) 整串命中
	if v, ok := table[q]; ok {
		return v
	}

	// 2) 包含命中：长键优先，避免"椿"这种单字误伤
	var keys []string
	for k := range table {
		if len([]rune(k)) >= 2 && strings.Contains(q, k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	longest := keys[0]
	for _, k := range keys {
		if len([]rune(k)) > len([]rune(longest)) {
			longest = k
		}
	}
	return table[longest]
}

// loadAliases 内置表 + 用户文件，用户文件覆盖内置（同键以用户为准）
func loadAliases(workDir string) map[string][]string {
	out := make(map[string][]string, len(builtinAliases))
	for k, v := range builtinAliases {
		out[normalizeAlias(k)] = v
	}
	if workDir == "" {
		return out
	}
	data, err := os.ReadFile(joinPath(workDir, aliasFile))
	if err != nil {
		return out
	}
	var custom map[string][]string
	if err := json.Unmarshal(data, &custom); err != nil {
		return out // 文件写坏了也不影响内置表
	}
	for k, v := range custom {
		out[normalizeAlias(k)] = v
	}
	return out
}

// normalizeAlias 归一化：小写、去空格/下划线/连字符、去常见修饰词
func normalizeAlias(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("_", "", " ", "", "-", "", "・", "").Replace(s)
	for _, noise := range []string{"的", "本子", "r18", "hentai", "色图"} {
		s = strings.ReplaceAll(s, noise, "")
	}
	return s
}

func joinPath(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}
