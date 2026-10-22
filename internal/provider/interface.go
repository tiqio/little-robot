package provider

import (
	"context"
	"little-robot/internal/schema"
)

// provider是一个翻译层，左面是schema.Message，右面是厂商的线格式（wire format），翻译只发生在Generate这一个方法里面
type LLMProvider interface {
	// 收历史和工具列表两个参数，不同厂商表达方式不同，DeepSeek用tools数组，Anthropic用tool_use block，转换责任放到Provider里面，engine只传递材料，这就是适配器模式
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) // 返回指针是因为Message以后会变大，只读不复制，ctx：加超/取消的时候就可以优化签名，防止改参数
}
