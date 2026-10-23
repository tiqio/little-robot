### 文档

- [飞书机器人接入指南（配置流程 + 踩坑 + Playwright 辅助）](./docs/feishu-setup.md)

### something

如果%有点问题：go vet ./...

gitignore：
1. /workspace/：只忽略根目录下的workspace/（推荐）
2. workspace/：忽略任何层级叫workspace的目录（包括internal/workspace）
3. workspace：连叫workspace的文件也忽略

### .env

```
DEEPSEEK_API_KEY=sk-xxx
DEEPSEEK_BASE_URL=https://api.deepseek.com
DEEPSEEK_MODEL=deepseek-flash
LLM_REQUEST_DEBUG=true
FEISHU_APP_ID=cli_xxx
FEISHU_APP_SECRET=xxx
ROBOT_WORKDIR=xxx (default=./workspace)
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

我们之前只开了「获取与发送单聊、群组消息」im:message，加上事件自动补的「群组里 @ 机器人的消息」。单聊消息这个细分权限漏了——所以群里 @ 能收到，单聊连输入框都不给。