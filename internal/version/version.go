package version

// Version 是 little-robot 的版本号，发布时通过
// go build -ldflags "-X little-robot/internal/version.Version=v1.2.3" 注入。
var Version = "dev"

// String 返回用于展示的版本字符串。
func String() string {
	return Version
}
