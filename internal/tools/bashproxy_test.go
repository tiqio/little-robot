package tools

import (
	"context"
	"testing"
)

func TestBashSeesProxy(t *testing.T) {
	dir := t.TempDir()
	tool := NewBashTool(dir)
	out, err := tool.Execute(context.Background(), []byte(`{"command":"echo $HTTPS_PROXY"}`))
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	t.Logf("子进程看到的 HTTPS_PROXY = %q", out)
}
