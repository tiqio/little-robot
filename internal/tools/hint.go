package tools

import "strings"

/*
hintFor：工具失败时追加一句"下一步该怎么改"。

为什么值得做（ch14 的思路）：
模型撞到工具报错后，常见的反应是"原样重试"或"换一种同样错的做法"，
白白烧掉一两轮。把"该怎么改"直接写进错误返回，等于替它把诊断做完。

设计约定：
- **不改原始报错**，只在后面追加，便于 grep 和排查；
- 匹配串优先用**系统级稳定文案**（no such file or directory 这类 POSIX 固定字符串），
  其次才是我们自己的工具文案——所以我们自己改文案时要同步改这里的表；
- 命中不了就返回空串，调用方保持原样（**不要硬编一个泛泛的提示**）。
*/

type hintRule struct {
	tool   string // 空串表示所有工具
	substr string // 已转小写，匹配时对 errText 也做小写比较
	hint   string
}

var hintRules = []hintRule{
	// ---- edit_file：最常见，也最容易让模型原地打转 ----
	{
		tool:   "edit_file",
		substr: "old_text在文件中出现了",
		hint: "old_text 不唯一。先用 read_file 读目标函数，把 old_text 从「一句话」扩展到" +
			"「函数签名 + 前后各 2 行」，让它只可能命中一处。",
	},
	{
		tool:   "edit_file",
		substr: "在文件中未找到 old_text",
		hint: "old_text 和文件当前内容不一致（常见原因：缩进不同、文件已被改过、有多余空行）。" +
			"必须先用 read_file 重新读一遍，照抄真实内容再改；不要凭记忆重写。",
	},
	{
		tool:   "edit_file",
		substr: "模糊匹配到了",
		hint:   "去掉缩进后仍有多个候选。把 old_text 往上/下多带几行，或改用 write_file 整文件重写。",
	},

	// ---- 文件不存在：模型最爱猜路径 ----
	{
		substr: "no such file or directory",
		hint: "路径不存在。不要猜文件名：先用 bash 执行 `ls -la` 或 `find . -name \"*关键字*\"` " +
			"确认真实路径，再用相对路径重试。",
	},
	{
		substr: "is a directory",
		hint:   "目标是个目录，不是文件。先 ls 看里面有什么，再指定到具体文件。",
	},

	// ---- 权限 ----
	{
		substr: "permission denied",
		hint:   "没有权限。不要尝试 sudo（会被护栏拦），换一个可写路径，或说明需要人工处理。",
	},

	// ---- bash：超时和命令缺失 ----
	{
		tool:   "bash",
		substr: "超时",
		hint: "命令超过 30 秒被杀。常驻服务（server/watch/tail -f）要用后台方式启动，例如 " +
			"`nohup cmd > out.log 2>&1 &`，不要阻塞主线程；大任务请拆成小步骤。",
	},
	{
		tool:   "bash",
		substr: "command not found",
		hint: "系统里没有这个命令。先 `which <cmd>` 确认，找不到就换等价命令（如 rg→grep、python3→python），" +
			"或先输出结果而不依赖它。",
	},
	{
		tool:   "bash",
		substr: "syntax error",
		hint: "shell 语法错误。检查引号是否成对、变量是否加了花括号、特殊字符是否转义；" +
			"复杂命令先写进脚本文件再执行。",
	},

	// ---- 网络：本项目里 curl/HTTP 都走代理，这里给排查线索 ----
	{
		substr: "no such host",
		hint: "域名解析失败。可能是网络需要代理：本项目已自动把探测到的代理传给子进程，" +
			"先用 `echo $HTTPS_PROXY` 确认；确认不了就换一个数据源。",
	},
	{
		substr: "connection refused",
		hint:   "连接被拒绝。确认目标服务是否在跑、端口是否正确；本地服务先看进程是否存在。",
	},
	{
		substr: "i/o timeout",
		hint:   "网络超时。不要反复重试同一个地址：换一个来源，或改用别的数据获取方式。",
	},

	// ---- 图片工具 ----
	{
		tool:   "fetch_image",
		substr: "不在白名单内",
		hint: "该域名不在白名单。photo 模式用 Pexels/Openverse/Wikimedia 的关键词搜索，" +
			"anime 模式用 Danbooru/Safebooru；不要自己去拼第三方图片站的 URL。",
	},
	{
		substr: "超过 10mb",
		hint: "文件超过 10MB 上限。先压缩或缩小尺寸（图片可用 `sips -Z 1600 in.jpg --out out.jpg`），" +
			"或换一个更小的资源。",
	},
}

// hintFor 返回救援提示；没有命中返回空串。
func hintFor(toolName, errText string) string {
	lower := strings.ToLower(errText)
	for _, r := range hintRules {
		if r.tool != "" && r.tool != toolName {
			continue
		}
		if strings.Contains(lower, r.substr) {
			return r.hint
		}
	}
	return ""
}
