package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// 真机回归（NETUTIL_LIVE=1 才跑）：webp 经过 fileToDataURL 之后，
// 本地 llama.cpp 必须接受，不能再 400。
func TestLiveWebPAcceptedByServer(t *testing.T) {
	if os.Getenv("NETUTIL_LIVE") != "1" {
		t.Skip("需要 NETUTIL_LIVE=1")
	}
	dir := t.TempDir()
	path := dir + "/tiny.webp"
	if err := os.WriteFile(path, tinyWebPBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}
	url, err := fileToDataURL(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("data URL 前缀：%.40s", url)

	reqBody, _ := json.Marshal(map[string]any{
		"model":      "huihui-qwen3.8-27b-abliterated@iq2_s",
		"max_tokens": 4,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "?"},
				{"type": "image_url", "image_url": map[string]any{"url": url, "detail": "auto"}},
			},
		}},
	})
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Post("http://127.0.0.1:1234/v1/chat/completions", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw bytes.Buffer
	_, _ = raw.ReadFrom(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("后端拒绝了转码后的图片（%d）：%s", resp.StatusCode, raw.String())
	}
	fmt.Println("OK：webp 转码后被本地服务接受")
}
