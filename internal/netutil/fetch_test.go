package netutil

import (
	"context"
	"os"
	"strings"
	"testing"
)

// 真实联网测试：只在 NETUTIL_LIVE=1 时跑，避免默认测试依赖网络
func TestFetchLive(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("设置 NETUTIL_LIVE=1 才跑联网测试")
	}
	t.Logf("proxy = %s", ProxyInfo())

	data, ctype, err := Fetch(context.Background(), "https://picsum.photos/400/300", 10<<20)
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	t.Logf("拿到 %d 字节，类型 %s", len(data), ctype)
	if !strings.HasPrefix(ctype, "image/") {
		t.Fatalf("期望图片类型，实际 %s", ctype)
	}
}
