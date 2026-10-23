---
name: go-test
description: 当需要允许或修复Go测试时使用
---

## 步骤
1. 先`go build ./...`，编译不过不要跑测试
2. 再`go test ./...`，把失败用例的完整输出贴出来
3. 只改导致失败的那一处，改完重跑同一个包：`go test ./internal/tools`
4. 三轮还不过就停下来报告，不要在失败用例上来回试