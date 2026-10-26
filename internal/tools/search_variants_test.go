package tools

import (
	"strings"
	"testing"
)

// 降级顺序：原样 → 去语言词 → 只留首词 → 下划线转空格
func TestSearchVariants(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{
			// 真实事故里的那个词
			in: "aysa wuthering_waves chinese",
			want: []string{"aysa wuthering_waves chinese", "aysa wuthering_waves", "aysa",
				"aysa wuthering waves chinese", "wuthering waves", "chinese"},
		},
		{in: "aemeath", want: []string{"aemeath"}},
		{in: "中文 汉化 爱弥斯", want: []string{"中文 汉化 爱弥斯", "爱弥斯", "中文", "汉化"}},
		{in: "   ", want: nil},
	}
	for _, c := range cases {
		got := searchVariants(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("searchVariants(%q) =\n %v\n期望\n %v", c.in, got, c.want)
		}
	}
}

// 全部试完仍 0 条：要给出"换成英文名/别加 chinese"这种可执行建议
func TestZeroResultHint(t *testing.T) {
	h := zeroResultHint("aysa wuthering_waves chinese", []string{"aysa wuthering_waves chinese", "aysa"})
	for _, want := range []string{"没有结果", "aemeath", "英文名", "chinese", "已自动试过"} {
		if !strings.Contains(h, want) {
			t.Errorf("提示里应该包含 %q：\n%s", want, h)
		}
	}
}
