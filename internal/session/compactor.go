package session

import (
	"fmt"
	"strings"

	"little-robot/internal/schema"

	"log"
)

// 压缩不是删除内容，而是换内容，如果把很久以前的工具结果整条删掉会出事，所以正确的做法是，消息一条不删除，为止不懂，只把它们的内容换成一句占位符
// tool："...[早期工具输出已经清理，原长1545]..."
// 结构完整 —— 请求永远合法；内容变短 —— token降下来；模型还直到这里曾经有结果

type Compactor struct {
	MaxChars   int // 字符数阈值（token的廉价近似）：历史总字符超过它，才开始压，现在设置的值是20000，约等于1万token，留余量给下一轮输出，注意中文约0.6~1token/字，英文0.25token/字，要的是趋势不是精度
	RetainLast int // 最近多少条字符保持原样，最近N条消息原样保留，一条都不栋，现在默认为8，模型一轮一般是思考+调用+结果三条，8条~最近两三轮
}

func NewCompactor(maxChars, retainLast int) *Compactor {
	return &Compactor{
		MaxChars:   maxChars,
		RetainLast: retainLast,
	}
}

func (c *Compactor) Compact(msgs []schema.Message) []schema.Message {
	if c.estimate(msgs) < c.MaxChars {
		return msgs // 判断当前msg是否需要被压缩，没有阈值就原样返回
	}
	return c.compact(msgs)
}

// ManualCompact 用户主动点"压缩上下文"：不看阈值，保护区也收紧到最近 8 条。
// 自动压缩是"别撞窗口"，手动压缩是"我现在就想省点 token"，后者应该更狠。
func (c *Compactor) ManualCompact(msgs []schema.Message) []schema.Message {
	manual := &Compactor{MaxChars: 0, RetainLast: 8}
	return manual.compact(msgs)
}

// Estimate 让外部（按钮回执、日志）能报告"压缩前多大"
func (c *Compactor) Estimate(msgs []schema.Message) int { return c.estimate(msgs) }

func (c *Compactor) compact(msgs []schema.Message) []schema.Message {

	// 先建立索引，后续打印的时候可以处理
	// 工具名只存在于assistant的ToolCalls里，工具结果消息本身就只有ToolCallID
	toolNames := make(map[string]string, len(msgs))
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			toolNames[tc.ID] = tc.Name
		}
	}

	// 保护区的七点：索引 >= protectFrom的消息不动内容
	protectFrom := len(msgs) - c.RetainLast
	if protectFrom < 0 {
		protectFrom = 0
	}

	out := make([]schema.Message, len(msgs))
	copy(out, msgs)

	var before, after int
	before = c.estimate(msgs)

	for i := range out {
		m := &out[i] // 指向副本里的第i条

		// system永远不动
		if m.Role == schema.RoleSystem {
			continue
		}

		origLen := len(m.Content)
		inWorkingMemory := i >= protectFrom // 在保护区里面的话就压得小一点，不在的话压的非常厉害，直接全部没掉了

		// 工具结果：老的拆解，近的头尾保留
		if m.Role == schema.RoleUser && m.ToolCallID != "" {
			if !inWorkingMemory {
				// 如果不在保护区里，小于200就不用压缩
				if origLen > 200 {
					m.Content = fmt.Sprintf("...[早期工具输出已清理，原长%d字节]...", len(m.Content))
				}
			} else if origLen > 1000 {
				// 保护区内太长：保留头500 + 尾500
				// 头部通常是命令回显，尾部通常是报错信息，中间是可以牺牲的
				// m.Content = m.Content[:500] + fmt.Sprintf("\n\n...[中间%d字节已截断]...\n\n", len(m.Content)-1000) + m.Content[len(m.Content)-500:]
				// m.Content = clipMiddle(m.Content, 500, 500)
				m.Content = clipMiddle(m.Content, 10000, 10000)
			}
		}

		// 老的图片消息（ch34/S3）：data URL 是 base64，一张几百 KB，
		// 留在历史里每轮都重新喂给模型。原图还在磁盘上，view_image 能找回来，
		// 所以历史里留一句"看过图"的占位就够
		if m.Role == schema.RoleUser && len(m.Images) > 0 && !inWorkingMemory {
			n := len(m.Images)
			m.Images = nil
			note := ImageNote(n)
			if strings.TrimSpace(m.Content) == "" {
				m.Content = note
			} else {
				m.Content += "\n" + note
			}
		}

		// 老的推理文本：折叠，带ToolCalls的消息Content可能为空，因此这里仅仅考虑到推理太长的情况
		if m.Role == schema.RoleAssistant && m.Content != "" && !inWorkingMemory && len(m.Content) > 200 {
			m.Content = "...[早期推理已折叠]..."
		}

		// 唯一打印日志的时候：只打印真的被该国的那几条
		if len(m.Content) != origLen {
			log.Printf("[Compactor] #%-2d %-16s %6d -> %-6d 字节", i, labelOf(*m, toolNames), origLen, len(m.Content))
		}
	}

	after = c.estimate(out)
	log.Printf("[Compactor] 上下文%d->%d字符（阈值%d，保护区%d条）", before, after, c.MaxChars, c.RetainLast)

	return out
}

func (c *Compactor) estimate(msgs []schema.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, tc := range m.ToolCalls {
			n += len(tc.Name) + len(tc.Arguments)
		}
		// 图片是 data URL 字符串，长度约等于它的 token 成本（之前完全没算，
		// 历史里躺 3 张图 compactor 也看不见，阈值永远不触发）
		for _, img := range m.Images {
			n += len(img)
		}
	}
	return n
}

// ImageNote "看过图"的一句话占位：compactor（压历史）、store（落盘剥图）、engine（剥图重试）共用同一句
func ImageNote(n int) string {
	return fmt.Sprintf("（此处查看过 %d 张图片，已省略图片内容）", n)
}

// labelOf 给日志用的可读标签：tool:bash / assitant /user
func labelOf(m schema.Message, toolNames map[string]string) string {
	if m.ToolCallID == "" {
		return string(m.Role)
	}
	if name, ok := toolNames[m.ToolCallID]; ok {
		return "tool:" + name
	}
	// 发起调用的assistant消息可能已经被窗口截掉，只剩ID（在msg损失情况下进行处理的流程）
	id := m.ToolCallID
	if len(id) > 8 {
		id = id[:8]
	}
	return "tool:?" + id
}

// clipMiddle保留头尾，中间折叠，按照rune切，避免把中文切成半个字
func clipMiddle(s string, head, tail int) string {
	r := []rune(s)
	// 保留head/tail字符，中间截断，没有问题
	if len(r) < head+tail {
		return s
	}
	return string(r[:head]) + fmt.Sprintf("\n\n...[中间%d字符已截断]...\n\n", len(r)-head-tail) + string(r[len(r)-tail:]) // rune是一个Unicode码点，就是一个字，string(r)在反向编码
}
