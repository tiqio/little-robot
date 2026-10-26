package session

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode/utf8"

	"little-robot/internal/schema"
)

// 压缩不是删除内容，而是换内容，如果把很久以前的工具结果整条删掉会出事，所以正确的做法是，消息一条不删除，为止不懂，只把它们的内容换成一句占位符
// tool："...[早期工具输出已经清理，原长1545]..."
// 结构完整 —— 请求永远合法；内容变短 —— token降下来；模型还直到这里曾经有结果

type Compactor struct {
	MaxChars   int // 字符数阈值（token的廉价近似）：历史总字符超过它，才开始压，现在设置的值是20000，约等于1万token，留余量给下一轮输出，注意中文约0.6~1token/字，英文0.25token/字，要的是趋势不是精度
	RetainLast int // 最近多少条字符保持原样，最近N条消息原样保留，一条都不栋，现在默认为8，模型一轮一般是思考+调用+结果三条，8条~最近两三轮
	// ClipHead/ClipTail 保护区内单条消息保留的头/尾字符数。
	// 之前写死 10000+10000：一条 108KB 的工具输出压完还有 60KB，
	// 30 条加起来 1.8MB，本地模型预填充要十几分钟——等于没压。
	ClipHead int
	ClipTail int
}

func NewCompactor(maxChars, retainLast int) *Compactor {
	return &Compactor{
		MaxChars:   maxChars,
		RetainLast: retainLast,
		ClipHead:   defaultClipHead,
		ClipTail:   defaultClipTail,
	}
}

// 自动压缩的保护区裁剪力度：8K 字符 ≈ 4K token，够模型看懂"这条大概是什么"
const (
	defaultClipHead = 4000
	defaultClipTail = 4000
)

// 用户点"压缩"：比自动更狠一点（保护区 8 条、每条留 2500+2500）
const (
	manualRetainLast = 8
	manualClipHead   = 2500
	manualClipTail   = 2500
)

// 上下文超限自救：不留情面，只保住最近 3 条、每条 1200+1200。
// 这时候的目标是"能发出请求"而不是"信息完整"——毕竟结果是失败还是继续，就差这一下。
const (
	squeezeRetainLast = 3
	squeezeClipHead   = 1200
	squeezeClipTail   = 1200
)

func (c *Compactor) Compact(msgs []schema.Message) []schema.Message {
	if c.estimate(msgs) < c.MaxChars {
		return msgs // 判断当前msg是否需要被压缩，没有阈值就原样返回
	}
	return c.compact(msgs)
}

// ManualCompact 用户主动点"压缩上下文"：不看阈值，保护区也收紧到最近 8 条。
// 自动压缩是"别撞窗口"，手动压缩是"我现在就想省点 token"，后者应该更狠。
func (c *Compactor) ManualCompact(msgs []schema.Message) []schema.Message {
	return (&Compactor{
		MaxChars:   0,
		RetainLast: manualRetainLast,
		ClipHead:   manualClipHead,
		ClipTail:   manualClipTail,
	}).compact(msgs)
}

// Squeeze 上下文超限时的"急救压缩"：能删的都删、能截的都截。
// 和 ManualCompact 的区别是它还允许丢细节——先让请求发得出去，再谈记忆。
func (c *Compactor) Squeeze(msgs []schema.Message) []schema.Message {
	return (&Compactor{
		MaxChars:   0,
		RetainLast: squeezeRetainLast,
		ClipHead:   squeezeClipHead,
		ClipTail:   squeezeClipTail,
	}).compact(msgs)
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

	// 图片是"体积炸弹"：一张缩略图的 data URL 十几万字符。
	// 不管在不在保护区，都只保留最近 keepRecentImages 条带图消息——
	// 模型只需要看"当前这张"，历史图片可以随时用 view_image 重新看。
	// 真实事故：每轮注入 1 张封面图，36 条保护区全留着，上下文涨到 55 万字符。
	const keepRecentImages = 2
	var imageIdxs []int
	for i := range msgs {
		if len(msgs[i].Images) > 0 {
			imageIdxs = append(imageIdxs, i)
		}
	}
	keepImage := map[int]bool{}
	if len(imageIdxs) > keepRecentImages {
		for _, i := range imageIdxs[len(imageIdxs)-keepRecentImages:] {
			keepImage[i] = true
		}
	} else {
		for _, i := range imageIdxs {
			keepImage[i] = true
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
			} else if origLen > c.ClipHead+c.ClipTail {
				// 保护区内太长：保留头尾，中间可以牺牲
				// 头部通常是命令回显，尾部通常是报错信息
				m.Content = clipMiddle(m.Content, c.ClipHead, c.ClipTail)
			}
		}

		// 老的图片消息（ch34/S3 + ch46）：data URL 是 base64，一张几百 KB，
		// 留在历史里每轮都重新喂给模型。原图还在磁盘上，view_image 能找回来，
		// 所以历史里留一句"看过图"的占位就够
		if m.Role == schema.RoleUser && len(m.Images) > 0 && (!inWorkingMemory || !keepImage[i]) {
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

// SqueezeTo 压到"预算之内"为止，返回（压缩结果, 是否用了折叠兜底）。
//
// 为什么需要预算而不是"保留最近 N 条"：保护区里的单条消息可能就有几十万字符
// （图片 data URL、write_file 的巨型参数），只保留 3 条照样 300K。
// 真实事故：压完 300936 → 300656，只掉了 280 字符，然后模型预填充 38 分钟。
func (c *Compactor) SqueezeTo(msgs []schema.Message, maxChars int) ([]schema.Message, bool) {
	out := c.Squeeze(msgs)
	if maxChars <= 0 || c.estimate(out) <= maxChars {
		return out, false
	}

	// 第一轮: 剥掉所有图片（data URL 是最常见的体积来源）
	stripped := StripImages(out)
	if c.estimate(stripped) <= maxChars {
		return stripped, false
	}

	// 第二轮: 连保护区也收成 1 条、头尾各 400 字符
	tight := (&Compactor{MaxChars: 0, RetainLast: 1, ClipHead: 400, ClipTail: 400}).compact(stripped)
	if c.estimate(tight) <= maxChars {
		return tight, false
	}

	// 第三轮: 折叠成一条摘要——宁可丢细节，也不能让请求发不出去
	return Summarize(stripped, maxChars), true
}

// Summarize 把整段历史折叠成"一句摘要 + 最后一条用户消息"。
// 这是最后一道闸：保证 prompt 一定小，而不是赌模型能读懂 30 万字符。
func Summarize(msgs []schema.Message, maxChars int) []schema.Message {
	var system []schema.Message
	var lastUser string
	var dropped int
	for _, m := range msgs {
		switch m.Role {
		case schema.RoleSystem:
			system = append(system, m)
			continue
		case schema.RoleUser:
			if m.ToolCallID == "" && strings.TrimSpace(m.Content) != "" {
				lastUser = m.Content
			}
		}
		dropped++
	}
	note := fmt.Sprintf("（上下文已折叠：前面 %d 条记录因为超出模型窗口被省略。如果你需要之前的细节，请让用户重新描述，或用工具重新获取。）", dropped)
	// 按**字节**截断最后那条用户消息：budget 是字符/字节口径，
	// 用 rune 截会因为中文 3 字节/字而超预算（真实踩过：预算 5000 压完 7694）
	if keep := maxChars - len(note) - 64; keep > 100 && len(lastUser) > keep {
		b := []byte(lastUser)[:keep]
		for len(b) > 0 && !utf8.Valid(b) {
			b = b[:len(b)-1]
		}
		lastUser = string(b) + "…"
	}
	if lastUser != "" {
		note += "\n\n当前请求：" + lastUser
	}
	out := append(system, schema.Message{Role: schema.RoleUser, Content: note})
	log.Printf("[Compactor] 已折叠成摘要：%d 条 → %d 条（%d 字符）", len(msgs), len(out), estimateOf(out))
	return out
}

// StripImages 剥掉所有图片，换成一句占位（任何位置都剥）
func StripImages(msgs []schema.Message) []schema.Message {
	out := make([]schema.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		if len(out[i].Images) == 0 {
			continue
		}
		n := len(out[i].Images)
		out[i].Images = nil
		note := ImageNote(n)
		if strings.TrimSpace(out[i].Content) == "" {
			out[i].Content = note
		} else {
			out[i].Content += "\n" + note
		}
	}
	return out
}

func estimateOf(msgs []schema.Message) int {
	return (&Compactor{}).estimate(msgs)
}

// TopMessages 返回体积最大的几条消息（角色 + 字节数），用于诊断"为什么这么大"
func TopMessages(msgs []schema.Message, n int) []string {
	type item struct {
		idx  int
		size int
		role schema.Role
	}
	items := make([]item, 0, len(msgs))
	for i, m := range msgs {
		size := len(m.Content)
		for _, tc := range m.ToolCalls {
			size += len(tc.Arguments) + len(tc.Name)
		}
		for _, img := range m.Images {
			size += len(img)
		}
		items = append(items, item{i, size, m.Role})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].size > items[j].size })
	if n > len(items) {
		n = len(items)
	}
	out := make([]string, 0, n)
	for _, it := range items[:n] {
		out = append(out, fmt.Sprintf("#%d %s %d 字符", it.idx, it.role, it.size))
	}
	return out
}
