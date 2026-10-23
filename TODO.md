# TODO：-version 参数

- [X] 1. 新建 `internal/version/version.go`：`var Version = "dev"` + `String()`
- [X] 2. 新建 `cmd/robot/cli.go`：`handleArgs` 解析 `-version` / `-h`
- [X] 3. 改 `cmd/robot/main.go`：main 开头装配调用 `handleArgs`，置于 env 加载之前
- [X] 4. `gofmt -l` 检查格式
- [X] 5. `go build ./...` 通过
- [X] 6. `go vet ./cmd/robot` + 实跑 `./robot.exe -version` 验证输出
