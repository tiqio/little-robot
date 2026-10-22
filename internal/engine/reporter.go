package engine

import (
	"context"
	"little-robot/internal/schema"
)

/*
	演进：从打印到发事件，这样可以对接很多的接收端，让引擎的职责在跑逻辑，而不是决定字打在哪里
	reporter.OnXxx(...)（引擎只喊一声有事情发生了），reporter谁接谁决定怎么展示
	这就叫观察者模式/依赖注入：引擎不关心谁在听，它只负责把事件广播出去
	关键设计选择有三个：
		1. 接口方法名是事件，不是打印函数：OnToolCall而不是PrintToolCall
		2. reporter作为Run的参数，不是结构体字段：因为飞书场景里，每个用户/每个绘画对应不同的回复通道，A用户的消息不能发到B的群里
			如果reporter是AgentEngine的字段，就意味着“一个引擎只能有一个出口”，多用户必须new多个引擎
		3. 引擎里log.Printf留着，fmt.Printf搬走
			log.Printf：开发者看，可以留在引擎里排查问题
			fmt.Printf：用户/模型交互，需要搬走，包含模型说的话，工具的输出
	区分开来后，生产环境可以关掉日志，但事件流照走
*/

// Reporter 是引擎向外部广播事件的出口
// 引擎只负责“发生了什么”，不负责“怎么展示”
type Reporter interface {
	// 进入思考阶段（还没有内容，只是个信号）
	OnThinkingStart(ctx context.Context)
	// 思考阶段产出的内容
	OnThinkingText(ctx context.Context, text string)
	// 对外回复的内容
	OnAssistantMessage(ctx context.Context, text string)
	// 即将执行一个工具调用
	OnToolCall(ctx context.Context, call schema.ToolCall)
	// 工具执行完毕（成功或者失败都走这里）
	OnToolResult(ctx context.Context, call schema.ToolCall, result schema.ToolResult)
}
