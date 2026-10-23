package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"little-robot/internal/schema"
	"log"
)

// 同一个工具+参数连续失败多少次后介入
const repeatFailureThreshold = 3

type Reminder struct {
	failures map[string]int
}

func NewReminder() *Reminder {
	return &Reminder{failures: make(map[string]int)}
}

// fingerprint 由工具名+参数算出，参数不同是不同的指纹
func fingerprint(call schema.ToolCall) string {
	h := sha256.New()
	h.Write([]byte(call.Name))
	h.Write([]byte{0}) // 分隔符，防止参数和名称撞车
	h.Write(call.Arguments)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Observe 记录本轮所有工具结果；返回nil表示该注入一条提醒
func (r *Reminder) Observe(calls []schema.ToolCall, results []schema.ToolResult) *schema.Message {
	if len(r.failures) > 1000 {
		r.failures = make(map[string]int)
	}

	// 只要有一个成功，就认为它在往前走，全部清零
	for _, res := range results {
		if !res.IsError {
			r.failures = make(map[string]int)
			return nil
		}
	}

	// 同一轮内，相同指纹只算一次尝试，计数的单位是尝试，不是调用，连续的判定是中间有没有成功过
	seen := make(map[string]struct{}, len(calls))
	worstIdx, worstCount := -1, 0

	for i, call := range calls {
		fp := fingerprint(call)

		if _, dup := seen[fp]; dup {
			continue // 已经在本轮计过了
		}
		seen[fp] = struct{}{}

		r.failures[fp]++
		if r.failures[fp] > worstCount {
			worstCount = r.failures[fp]
			worstIdx = i
		}
	}

	if worstIdx < 0 || worstCount < repeatFailureThreshold {
		return nil
	}

	name := calls[worstIdx].Name
	args := string(calls[worstIdx].Arguments)

	// 命中后清零：否则下一轮还会发送同一条警告
	r.failures = make(map[string]int)

	log.Printf("[Reminder] 介入：%s用相同参数连续%d次", name, worstCount)

	return &schema.Message{
		Role: schema.RoleUser,
		Content: fmt.Sprintf(`[系统提醒] 你连续%d次用同一组参数调用%s，全部失败
		
最后一次调用的参数是：
%s

立即停止这个重试，你的注意力被当前报错粘住了，接下来做其中一件事：
1. 换一个完全不同的思路（先bash的ls看清环境，再动手）
2. 换一组不同的参数
3. 如果确实做不到，直接停下并说明你缺什么，不要继续消耗调用次数`, worstCount, name, args),
	}
}
