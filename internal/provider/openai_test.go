package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
