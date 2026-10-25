# 工作目录隔离

> 解决的问题：bot 同时服务多个群时，**群 A 写的文件群 B 能读、能改、能删**。
> 本文记录这套机制的设计、实现位置和边界。

---

## 0. 三层概念，别混

| 层 | 是什么 | 生命周期 | 代码位置 |
|---|---|---|---|
| **档案柜** | 对话历史，跨消息、跨重启 | 长期 | `internal/session`（`sessions/<id>.json`） |
| **台面** | 本次运行的可变状态 | 一次 `Run` | `internal/runstate`（走 ctx 传，**不落盘**） |
| **目录** | 文件系统落点 | 由策略决定 | `internal/feishu/bot.go` 的 `workDirFor` |

**为什么台面不能塞进档案柜**：审批状态（`Approved`）一旦被 `Save` 持久化，重启后就"永久有效"——
等于没人批准过也能执行。**运行态必须一次性。**

---

## 1. 两种目录策略

`workDirFor` 内部按顺序尝试：

| 顺序 | 策略 | 条件 | 结果路径 |
|---|---|---|---|
| 1 | **git worktree** | 工作区本身是 git 仓库根 | `<repo>/.worktrees/chat-<名>-<短id>`（独立分支 `wt/chat-...`） |
| 2 | **普通子目录**（兜底） | 其余所有情况 | `<workDir>/chats/<名>-<短id>` |

判断"仓库根"用 `isRepoRoot`：

```go
root, _ := worktree.RepoRoot(dir)     // 可能是父目录
return os.SameFile(stat(root), stat(dir))   // 必须是自己，不能是父目录
```

> ⚠️ **不要用字符串比较**。macOS 上 `/tmp` → `/private/tmp` 是软链，`git` 返回解析后的路径，
> 字符串永远对不上，会导致 worktree 永远不启用而只留一行日志。
> `os.SameFile` 比的是 inode，顺带解决大小写不敏感文件系统。

**为什么不直接在整个工作区上开 worktree**：如果 `workDir` 只是某个仓库里的子目录，
`git worktree add` 会作用在**外层仓库**上——等于在别人的项目里建分支、把整个项目检出过来。
所以必须要求"工作区就是仓库根"。

---

## 2. 目录命名

```
chats/
  dd-f1a826/          ← 群名 "dd" + chat_id 前 6 位
    poems/冬天的诗.md
  dm-3c7e91/          ← 单聊没有群名，退化成 dm-<短id>
```

规则：

1. **群名只在首次出现时算一次**，结果写进 `<workDir>/.claw/chats.json`；
2. 之后**永不改变**——群改名不会让目录搬家（否则之前写的文件会"消失"，其实留在旧目录）；
3. 名字清洗：只保留中文/字母/数字/`-_`，其余字符换成 `-`，限长 24 字符；
4. 永远带 `-<短id>` 后缀：两个群同名不会撞，也不怕群名被改成空。

`chats.json` 长这样，可以直接编辑：

```json
{
  "oc_f1a82656ab490474f7c111803fdfc49f": "dd-f1a826"
}
```

想给某个群换个目录名：改这里 + `mv` 对应目录。**两件事必须一起做。**

---

## 3. 工具怎么拿到目录

工具在 `main` 里构造，**不知道**本次运行在哪个目录。所以：

```go
// internal/tools/workdir.go
func effectiveWorkDir(ctx context.Context, fallback string) string {
	if rs := runstate.From(ctx); rs != nil && rs.WorkDir != "" {
		return rs.WorkDir
	}
	return fallback
}
```

每个工具入口一行：

```go
dir := effectiveWorkDir(ctx, t.workDir)
full, err := resolvePath(dir, in.Path)     // 文件类
cmd.Dir = effectiveWorkDir(ctx, t.workDir) // bash
```

> **加新工具时务必用 `effectiveWorkDir`**。漏一个就会出现"一半工具在新目录、一半在旧目录"，
> 典型症状是 `read_file` 读到了、`edit_file` 报"文件不存在"。
>
> 自查：`grep -rn "resolvePath(t.workDir" internal/tools/` 应该没有输出。

**子代理自动继承**：`spawn_subagent` 拿的是同一个 ctx，所以子代理也在同一个目录里干活。

---

## 4. 边界（重要）

### 4.1 它不是安全沙箱

```go
cmd.Dir = 新目录
```

只决定**起始**目录。命令里 `cd /Users/dd/AITEST/little-robot && git checkout .` 照样跑得出去。

- 文件类工具（`read_file` / `write_file` / `edit_file` / `send` / `fetch_image` / `fetch_hentai` / `view_image`）
  受 `resolvePath` 约束，**跑不出目录**；
- `bash` 只受起始目录约束，**真正的越权防护要靠 `guard` + 沙箱策略**。

### 4.2 共享只读区没有

所有群各自一份目录，**看不到别人的文件**——这解决了隔离，但也意味着"群 A 查到的资料群 B 用不了"。
需要共享时，把公共资料放在 `workDir` 根下，并让 `resolvePath` 支持"只读根"（当前未实现）。

### 4.3 worktree 里的未提交改动

`git worktree add` 只检出**某个提交**，工作区里未提交的改动**不会**带进新目录。
所以"新任务从干净状态开工"是默认行为。

---

## 5. 排障

| 现象 | 原因 | 处理 |
|---|---|---|
| 所有群写同一个目录 | `RunState` 没注入（老代码里曾有 `if worktreeMgr != nil` 才注入的条件） | 改成无条件注入，让 `workDirFor` 自己降级 |
| 同一个群两次跑到不同目录 | `Create` 第二次报"已存在"，代码 fallback 到子目录 | 用 `Ensure`（有则复用） |
| 目录名是 `oc_xxx` | 没开 `im:chat:readonly`，拿不到群名 | 开权限 + 重新发版 |
| 群改名后目录"空了" | 目录名跟着群名变 | 用 `chats.json` 固化映射 |

看日志确认走了哪条路：

```
[Feishu] 工作区 .../workspace 不是git仓库根，不启用worktree（用普通子目录隔离）
[Feishu] 为会话 dd-f1a826 新建 worktree：.../.worktrees/chat-dd-f1a826
```
