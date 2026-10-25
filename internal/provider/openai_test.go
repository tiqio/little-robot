package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const stopBody = `{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`

// cannedServer 返回固定响应体的 /chat/completions，reqCapture 非空时捕获请求体
func cannedServer(t *testing.T, body string, reqCapture *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reqCapture != nil {
			b, _ := io.ReadAll(r.Body)
			*reqCapture = b
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// finish_reason 必须透传到 schema.Message：引擎靠它区分"做完了"和"被截断了"
func TestFinishReasonPassThrough(t *testing.T) {
	cases := []struct {
		resp string
		want string
	}{
		{`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"length"}]}`, "length"},
		{`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, "stop"},
		{`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"tool_calls"}]}`, "tool_calls"},
		{`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`, ""}, // 老后端不返回时留空
	}
	for _, c := range cases {
		srv := cannedServer(t, c.resp, nil)
		p := NewOpenAI(Config{Name: "test", BaseURL: srv.URL, Model: "m"})

		out, err := p.Generate(context.Background(), nil, nil)
		if err != nil {
			t.Fatalf("Generate 失败：%v", err)
		}
		if out.FinishReason != c.want {
			t.Errorf("finish_reason = %q，期望 %q（响应：%s）", out.FinishReason, c.want, c.resp)
		}
	}
}

// 本地默认 2048（思考型模型一次能想 2000+ token，1024 会被思考吃光）
func TestLMStudioMaxTokensDefault(t *testing.T) {
	t.Setenv("LOCAL_MAX_TOKENS", "")
	var reqBody []byte
	srv := cannedServer(t, stopBody, &reqBody)
	p := NewLMStudioProvider(srv.URL, "m")

	if _, err := p.Generate(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(reqBody, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.MaxTokens != 2048 {
		t.Errorf("max_tokens = %d，期望 2048", parsed.MaxTokens)
	}
}

// LOCAL_MAX_TOKENS 可覆盖默认值
func TestLMStudioMaxTokensEnv(t *testing.T) {
	t.Setenv("LOCAL_MAX_TOKENS", "3333")
	var reqBody []byte
	srv := cannedServer(t, stopBody, &reqBody)
	p := NewLMStudioProvider(srv.URL, "m")

	if _, err := p.Generate(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(reqBody, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.MaxTokens != 3333 {
		t.Errorf("max_tokens = %d，期望 3333", parsed.MaxTokens)
	}
}

// WithMaxTokens 单次覆盖：思考段/行动段用不同额度，不动 provider 默认
func TestWithMaxTokensOverride(t *testing.T) {
	var reqBody []byte
	srv := cannedServer(t, stopBody, &reqBody)
	p := NewOpenAI(Config{Name: "test", BaseURL: srv.URL, Model: "m", MaxTokens: 999})

	ctx := WithMaxTokens(context.Background(), 555)
	if _, err := p.Generate(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(reqBody, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.MaxTokens != 555 {
		t.Errorf("覆盖后 max_tokens = %d，期望 555（provider 默认 999 应被顶掉）", parsed.MaxTokens)
	}

	// 不覆盖时回落 provider 默认
	if _, err := p.Generate(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(reqBody, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.MaxTokens != 999 {
		t.Errorf("不覆盖时 max_tokens = %d，期望回落到 999", parsed.MaxTokens)
	}
}

// NoThink → 请求体带 chat_template_kwargs.enable_thinking=false（LOCAL_NO_THINK 逃生门）
func TestNoThinkKwargs(t *testing.T) {
	var reqBody []byte
	srv := cannedServer(t, stopBody, &reqBody)
	p := NewOpenAI(Config{Name: "test", BaseURL: srv.URL, Model: "m", NoThink: true})

	if _, err := p.Generate(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Kwargs map[string]interface{} `json:"chat_template_kwargs"`
	}
	if err := json.Unmarshal(reqBody, &parsed); err != nil {
		t.Fatal(err)
	}
	if v, ok := parsed.Kwargs["enable_thinking"]; !ok || v != false {
		t.Errorf("enable_thinking = %v（ok=%v），期望 false", v, ok)
	}
}

// reasoning_content 透传到 schema.Message.Reasoning（思考吃了多少预算就看它）
func TestReasoningContentPassThrough(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi","reasoning_content":"let me think hard"},"finish_reason":"stop"}]}`
	srv := cannedServer(t, body, nil)
	p := NewOpenAI(Config{Name: "test", BaseURL: srv.URL, Model: "m"})

	out, err := p.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Reasoning != "let me think hard" {
		t.Errorf("Reasoning = %q，期望 'let me think hard'", out.Reasoning)
	}
	if out.Content != "hi" {
		t.Errorf("Content = %q，期望 'hi'", out.Content)
	}
}

// 后端因为图片格式拒绝（llama.cpp 的真实文案）→ 必须识别成 ErrImageRejected，
// 引擎才能剥图重试；识别不出来就是"一张图把任务跑死"
func TestImageRejectionDetected(t *testing.T) {
	bodies := []string{
		`{"error":"'url' field must be a base64 encoded image."}`,
		`{"error":{"code":400,"message":"Failed to load image or audio file"}}`,
		`{"error":"Invalid url."}`,
		`{"error":"bad image_url"}`,
	}
	for _, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(body))
		}))
		p := NewOpenAI(Config{Name: "lmstudio", BaseURL: srv.URL, Model: "m"})
		_, err := p.Generate(context.Background(), nil, nil)
		srv.Close()

		if !errors.Is(err, ErrImageRejected) {
			t.Errorf("响应 %s 应该识别成拒图，实际 err=%v", body, err)
		}
	}
}

// 普通 400（跟图片无关）不能被误判成拒图，否则会白白剥图重试
func TestNormal400NotImageRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"max_tokens is too large"}`))
	}))
	defer srv.Close()

	p := NewOpenAI(Config{Name: "lmstudio", BaseURL: srv.URL, Model: "m"})
	_, err := p.Generate(context.Background(), nil, nil)
	if errors.Is(err, ErrImageRejected) {
		t.Errorf("普通 400 不该被当成拒图：%v", err)
	}
	// 错误文案应该带 provider 名，而不是硬编码 DeepSeek
	if !strings.Contains(err.Error(), "lmstudio") {
		t.Errorf("错误信息应该带 provider 名，实际：%v", err)
	}
}
