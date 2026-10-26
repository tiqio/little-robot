### 文档

- [飞书机器人接入指南（配置流程 + 踩坑 + Playwright 辅助）](./docs/feishu-setup.md)
- [飞书控制台：压缩 / 清理 / 停止（按钮 + 文本命令 + 自检）](./docs/feishu-control.md)
- [工作目录隔离（RunState / worktree / 群名目录）](./docs/workdir-isolation.md)

### something

如果%有点问题：go vet ./...

gitignore：
1. /workspace/：只忽略根目录下的workspace/（推荐）
2. workspace/：忽略任何层级叫workspace的目录（包括internal/workspace）
3. workspace：连叫workspace的文件也忽略

### .env

```
# true=本地(LM Studio，不需要 key) / false=云端(DeepSeek)
# 切换模型只改 USE_LOCAL，然后重启进程

# ===== 模型：本地 or 云端 =====
USE_LOCAL=false
LOCAL_BASE_URL=http://127.0.0.1:1234/v1
LOCAL_MODEL=huihui-qwen3.8-27b-abliterated@iq2_s
REMOTE_BASE_URL=https://api.deepseek.com
REMOTE_MODEL=deepseek-flash
REMOTE_API_KEY=sk-xxx

# ===== 飞书 =====
FEISHU_APP_ID=cli_xxx
FEISHU_APP_SECRET=xxx

# ===== 工作区与调试 =====
ROBOT_WORKDIR=./workspace
LLM_REQUEST_DEBUG=false
```

### runner

```
go run ./cmd/robot -p "记住：代号蓝鲸，只回答收到" -dir /tmp/robot-demo -session demo
go run ./cmd/robot -p "代号是什么？只回答代号" -dir /tmp/robot-demo -session demo

rm -rf /tmp/robot-plan && go run ./cmd/robot \
  -p "在 workspace 里创建 hello.txt 写入 hello world" \
  -dir /tmp/robot-plan -session plan-demo
  
cat > /tmp/robot-plan/workspace/TODO.md <<'EOF'
# TODO
- [X] 创建 hello.txt
- [ ] 写一个 README.md 说明这个目录
EOF

go run ./cmd/robot -p "继续" -dir /tmp/robot-plan -session plan-demo
期望：它读TODO.md，从那个没打勾的开始干，而不是从头再来，这才是Plan Mode的真正价值——换进程、换事件、换人，都能接着干
```

### robot

机器人聊天窗口没有出现对话框（输入框）：应用机器人必须订阅接收消息事件，并且要开通 im:message.p2p_msg:readonly（读取用户发给机器人的单聊消息） 权限。

① 单聊：天然“一人一份记忆” 
```
用户 A ↔ 机器人   chat_id = oc_A → .claw/sessions/oc_A.json
用户 B ↔ 机器人   chat_id = oc_B → .claw/sessions/oc_B.json
```
A 和 B 各自有独立的历史，互不干扰。这是对的。

② 群聊：全群共享一份记忆
```
群里 A 说“记住代号蓝鲸”
群里 B 问“代号是什么” → 机器人答“蓝鲸”
```

我们之前只开了「获取与发送单聊、群组消息」im:message，加上事件自动补的「群组里 @ 机器人的消息」。单聊消息这个细分权限漏了——所以群里 @ 能收到，单聊连输入框都不给。
1. 用户把会话删了、重装应用、或换了个单聊入口，chat_id 有可能变；
2. 而 open_id 是“这个人对这个应用”的稳定标识。