# TODO：-version 参数

- [X] 1. 新建 `internal/version/version.go`：`var Version = "dev"` + `String()`
- [X] 2. 新建 `cmd/robot/cli.go`：`handleArgs` 解析 `-version` / `-h`
- [X] 3. 改 `cmd/robot/main.go`：main 开头装配调用 `handleArgs`，置于 env 加载之前
- [X] 4. `gofmt -l` 检查格式
- [X] 5. `go build ./...` 通过
- [X] 6. `go vet ./cmd/robot` + 实跑 `./robot.exe -version` 验证输出

## 第二轮：盘点 NewAgentEngine 调用点

- [X] 1. grep 全仓定位所有 NewAgentEngine 出现位置
- [X] 2. 读 cmd/robot/main.go 与 cmd/bot/main.go 上下文，确认参数与用途
- [X] 3. 读 internal/engine/loop.go 确认签名与定义处，排除非调用项
- [X] 4. 汇总回复（引用行号 + 用途）
