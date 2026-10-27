package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// ServerConfig 对应 mcp.json 里一个服务端条目。
/*
	{
	  "servers": {
	    "fs": { "command": "cmd", "args": ["/c", "mcp-server-filesystem", "."] }
	  }
	}

	Windows 提示：npx / mcp-server-* 是 .cmd 垫片，exec.Command 直接起会失败，
	必须写 "cmd" + ["/c", "..."]，或者填 .exe 的绝对路径。
*/
type ServerConfig struct {
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
}

// NamedServer 是"名字 + 配置"，名字会变成工具名前缀 mcp__<name>__<tool>。
type NamedServer struct {
	Name   string
	Config ServerConfig
}

// LoadConfig 读 mcp.json。
func LoadConfig(path string) ([]NamedServer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读 MCP 配置 %s：%w", path, err)
	}

	var file struct {
		Servers map[string]ServerConfig `json:"servers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("解析 MCP 配置 %s：%w", path, err)
	}
	if len(file.Servers) == 0 {
		return nil, fmt.Errorf("MCP 配置 %s 里没有 servers", path)
	}

	out := make([]NamedServer, 0, len(file.Servers))
	for name, cfg := range file.Servers {
		if cfg.Command == "" {
			return nil, fmt.Errorf("MCP 服务端 %q 缺少 command", name)
		}
		out = append(out, NamedServer{Name: name, Config: cfg})
	}
	return out, nil
}

// StartAll 拉起所有服务端。单个失败只记日志，不影响其它 server。
func StartAll(ctx context.Context, path string) ([]*Client, error) {
	servers, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}

	var clients []*Client
	for _, s := range servers {
		timeout := time.Duration(s.Config.TimeoutSeconds) * time.Second
		c, err := Start(ctx, s.Name, s.Config.Command, s.Config.Args, s.Config.Env, timeout)
		if err != nil {
			log.Printf("[MCP] 启动 %s 失败：%v", s.Name, err)
			continue
		}
		clients = append(clients, c)
	}
	return clients, nil
}
