# PLAN：cmd/robot 支持 -version 参数

## 需求理解
- `cmd/robot` 增加 `-version` 参数，打印版本号后退出（退出码 0）。
- 版本号来源：`var Version = "dev"`，保持可被 `-ldflags "-X ..."` 注入。
- 用户说的「/version 子命令」，在 CLI 语义下用 `-version` flag 实现（Go 惯例）。
- 改完 `go build ./...` 必须能过。

## 关键约束（来自 AGENTS.md）
1. `main.go` 只做装配，不写业务逻辑 → 参数解析放到 `cmd/robot/cli.go`。
2. 错误必须用 `fmt.Errorf("...: %w", err)` 包装。
3. 不改 `internal/schema`（本任务不涉及）。

## 技术选型
- `internal/version/version.go`：`var Version = "dev"` + `func String() string`。
  - 放在 internal 包，方便被注入与复用：`-ldflags "-X little-robot/internal/version.Version=v1.0.0"`。
- `cmd/robot/cli.go`：`func handleArgs(args []string) (handled bool, err error)`，
  用 `flag.NewFlagSet` 解析 `-version` / `-help`，不污染全局 flag。
- `main()` 开头（**在 LoadEnvFile / API Key 校验之前**）调用它；
  先处理 `-version` 可以保证没有 `.env`、没有 API Key 时也能打印版本。

## 行为契约
| 调用 | 行为 |
| --- | --- |
| `robot -version` | 打印 `little-robot dev`，退出 0，**不读 .env、不建 engine** |
| `robot -h` / 未知参数 | 打印用法到 stderr，退出 2 |
| `robot`（无参数） | 保持原有行为 |
