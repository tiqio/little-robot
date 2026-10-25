package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

/*
LM Studio 的上下文长度不是"请求里传的参数"，而是**加载模型时**定下的。
所以只能反问它：GET /api/v0/models 会告诉我们每个模型
max_context_length（上限）和 loaded_context_length（当前实际加载的）。

为什么值得问：压缩阈值必须跟着真实窗口走。
- 按 128K 算、但实际只加载了 8K → 等到 128K 才压缩，中途直接超窗
- 按 8K 算、实际加载了 128K → 压得太早，把能留的原话扔了
*/

// LMStudioContext 返回该模型实际加载的上下文长度；查不到返回 0。
func LMStudioContext(ctx context.Context, baseURL, model string) int {
	root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/api/v0/models", nil)
	if err != nil {
		return 0
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}

	var out struct {
		Data []struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			Max    int    `json:"max_context_length"`
			Loaded int    `json:"loaded_context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0
	}

	// 先精确匹配当前模型；匹配不到就退而求其次，取任意一个已加载的
	pick := func(m struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		Max    int    `json:"max_context_length"`
		Loaded int    `json:"loaded_context_length"`
	}) int {
		if m.Loaded > 0 {
			return m.Loaded
		}
		return m.Max
	}

	for _, m := range out.Data {
		if m.ID == model {
			return pick(m)
		}
	}
	for _, m := range out.Data {
		if m.State == "loaded" {
			return pick(m)
		}
	}
	return 0
}

var _ = fmt.Sprintf
