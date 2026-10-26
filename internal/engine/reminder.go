package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"little-robot/internal/schema"
)

// 同一个工具+参数连续多少次后开始提示 / 直接停下。
//
// 为什么不能只看"失败"：真实事故是模型用同一句 query 搜了 80 次，
// 每次都**成功返回**（只是 0 条结果），旧实现里"非 error 就清零"，
// 于是检测形同虚设，一路烧到 maxTurns=300。
// 打转的本质不是"报错"，是"同一个动作重复做、局面没变化"。
const (
	repeatHintThreshold  = 3 // 第 3 次同参数：注入一条"换策略"提醒
	repeatAbortThreshold = 6 // 第 6 次：直接收尾，别烧 token
)

type Reminder struct {
	lastFP string // 上一次工具调用的指纹（本轮最后一个）
	streak int    // 连续相同指纹的次数
}

func NewReminder() *Reminder { return &Reminder{} }

// fingerprint 由工具名+参数算出，参数不同是不同的指纹
func fingerprint(call schema.ToolCall) string {
	h := sha256.New()
	h.Write([]byte(call.Name))
	h.Write([]byte{0}) // 分隔符，防止参数和名称撞车
	h.Write(call.Arguments)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Observe 记录本轮工具调用；返回（要注入的提醒, 是否该终止）。
// 一旦出现不同指纹，说明模型换了动作，连续计数重置——这是"真的在往前走"的信号。
func (r *Reminder) Observe(calls []schema.ToolCall, results []schema.ToolResult) (nudge *schema.Message, abort bool) {
	for _, call := range calls {
		fp := fingerprint(call)
		if fp == r.lastFP {
			r.streak++
		} else {
			r.lastFP, r.streak = fp, 1
		}

		if r.streak >= repeatAbortThreshold {
			return nil, true
		}
		if r.streak == repeatHintThreshold {
			nudge = &schema.Message{
				Role: schema.RoleUser,
				Content: fmt.Sprintf(
					"（系统提醒）你已经用完全相同的参数调用 %s 三次了，结果没有变化。\n"+
						"别再试同一个词：换一个关键词（比如角色的英文名）、换一个工具，或者直接告诉用户没找到。\n"+
						"如果刚才是限流/超时，等一会儿再试一次是可以的。", call.Name),
			}
		}
	}
	return nudge, false
}

// Streak 当前连续次数（日志/测试用）
func (r *Reminder) Streak() int { return r.streak }

// AbortMessage 打转被强制终止时发给用户的话
func AbortMessage(toolName string, streak int) string {
	return fmt.Sprintf("（我在 %s 上连续试了 %d 次、参数都没变，先停下了。换个说法，或者直接给我本子编号/链接？）",
		toolName, streak)
}
