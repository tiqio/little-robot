package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"little-robot/internal/config"
	"log"
	"net/http"
	"os"
	"time"
)

type toolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type reqBody struct {
	Model    string `json:"model"`
	Messages []msg  `json:"messages"`
	Tools    []tool `json:"tools"`
}

type respToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type respMessage struct {
	Content   string         `json:"content"`
	ToolCalls []respToolCall `json:"tool_calls"`
}

func main() {
	_ = config.LoadEnvFile(".env")

	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		log.Fatal("DEEPSEEK_API_KEY需要被设置")
	}
	baseURL := os.Getenv("DEEPSEEK_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	model := os.Getenv("DEEPSEEK_MODEL")
	if model == "" {
		model = "deepseek-flash"
	}

	body := reqBody{
		Model:    model,
		Messages: []msg{{Role: "user", Content: "北京今天天气怎么样？"}},
		Tools: []tool{
			{
				Type: "function",
				Function: toolFunction{
					Name:        "get_weather",
					Description: "查询指定城市的天气",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]any{
								"type":        "string",
								"description": "城市名",
							},
							// "required": []string{"city"}, JSON Schema里面type，properties，required三个兄弟，现在告诉模型，"这个对象有两个属性，一个是city，另一个是required，而required的类型是数组"——属性必须是对象或者布尔（schema本身），所以目前数据不合法
						},
						"required": []string{"city"},
					},
				},
			},
		},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		log.Fatalf("序列化请求失败：%v", err)
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		log.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{
		Timeout: 60 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("请求失败：%v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("读响应失败：%v", err)
	}

	fmt.Println("HTTP", resp.StatusCode)
	fmt.Println(string(respBody))
	if resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}

	var parsed struct {
		Choices []struct {
			Message respMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		log.Fatalf("解析响应失败：%v", err)
	}
	if len(parsed.Choices) == 0 {
		log.Fatal("响应里面没有choices")
	}

	m := parsed.Choices[0].Message
	fmt.Println("----- 结论 -----")
	fmt.Printf("content = %q\n", m.Content) // 可以输出用双引号引起来的，安全的字符串，注意这个是从API获取到的需要包一下
	fmt.Printf("tool_calls = %d个\n", len(m.ToolCalls))
	for _, tc := range m.ToolCalls {
		fmt.Printf("	%s -> %s(%s)\n", tc.ID, tc.Function.Name, tc.Function.Arguments)
	}
}
