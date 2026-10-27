package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"little-robot/internal/version"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Client 是一个 MCP 服务端进程的句柄。并发安全：引擎会并行调工具。
type Client struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	timeout time.Duration

	mu      sync.Mutex // 保护 nextID / pending / 写 stdin
	nextID  int64
	pending map[int64]chan rpcMessage

	dead chan struct{} // readLoop 退出时关闭，用来叫醒所有等待者

	serverName string
	capability map[string]any
}

// Start 拉起服务端进程并完成握手。握手失败会清场，不留孤儿进程。
// timeout 是单次请求上限；<=0 用默认 120s（工具可能很慢）。
func Start(ctx context.Context, name, command string, args []string, env map[string]string, timeout time.Duration) (*Client, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("取 stdin：%w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("取 stdout：%w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("取 stderr：%w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 %s：%w", command, err)
	}

	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	c := &Client{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		timeout: timeout,
		pending: make(map[int64]chan rpcMessage),
		dead:    make(chan struct{}),
	}

	// 读 stdout 只能有一个 goroutine，否则两个 Scanner 会抢字节
	go c.readLoop(stdout)
	// stderr 是服务端的日志，和协议无关，单独转发
	go c.logStderr(stderr)

	if err := c.Initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Name 返回配置里的短名，用作工具名前缀，也用于日志。
func (c *Client) Name() string { return c.name }

// Initialize 走握手：initialize → notifications/initialized。
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "little-robot", "version": version.String()},
	}

	var res struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := c.request(ctx, "initialize", params, &res); err != nil {
		return fmt.Errorf("initialize：%w", err)
	}

	if res.ProtocolVersion != ProtocolVersion {
		log.Printf("[mcp:%s] 协议版本不一致：本机 %s，服务端 %s", c.name, ProtocolVersion, res.ProtocolVersion)
	}

	c.capability = res.Capabilities
	c.serverName = res.ServerInfo.Name + "@" + res.ServerInfo.Version

	// 规范要求握手后立刻发这条通知，否则有些服务端不接后续请求
	c.notify("notifications/initialized", nil)
	log.Printf("[mcp:%s] 已连接 %s", c.name, c.serverName)
	return nil
}

// HasTools 判断服务端是否声明了 tools 能力；没有就不要浪费时间拉列表。
func (c *Client) HasTools() bool {
	if c.capability == nil {
		return false
	}
	_, ok := c.capability["tools"]
	return ok
}

// ListTools 拉全部工具，处理分页（服务端可能一次只给一页）。
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	var all []ToolDef
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}

		var res struct {
			Tools      []ToolDef `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := c.request(ctx, "tools/list", params, &res); err != nil {
			return nil, fmt.Errorf("tools/list：%w", err)
		}

		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return all, nil
}

// CallTool 调一个工具。arguments 必须是 JSON 对象；模型给空/非法值时退回 {}，
// 让远端去报参数错误（错误信息更贴近它的 schema）。
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error) {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || !json.Valid(trimmed) {
		args = json.RawMessage(`{}`)
	}

	params := map[string]any{
		"name":      name,
		"arguments": args, // json.RawMessage 会被原样嵌入，不会二次转义
	}

	var res CallResult
	if err := c.request(ctx, "tools/call", params, &res); err != nil {
		return nil, fmt.Errorf("tools/call %s：%w", name, err)
	}
	return &res, nil
}

// Close 先关 stdin 让对端自己退，2 秒不退再 kill。
func (c *Client) Close() error {
	_ = c.stdin.Close()

	done := make(chan struct{})
	go func() {
		_ = c.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-done
	}
	return nil
}

// ---- 内部 ----

func (c *Client) newID() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// writeLine 写一行 JSON。必须加锁：引擎并行调工具时会同时写 stdin。
func (c *Client) writeLine(msg rpcMessage) error {
	msg.JSONRPC = "2.0"
	line, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.stdin.Write(line)
	return err
}

// notify 是"发了不等回"的消息（notifications/initialized、cancelled）。
func (c *Client) notify(method string, params any) {
	var raw json.RawMessage
	if params != nil {
		if b, err := json.Marshal(params); err == nil {
			raw = b
		}
	}
	_ = c.writeLine(rpcMessage{Method: method, Params: raw})
}

// respondError 回一个错误给服务端发来的请求（我们没实现 roots/sampling）。
func (c *Client) respondError(id int64, code int, message string) {
	_ = c.writeLine(rpcMessage{ID: &id, Error: &rpcError{Code: code, Message: message}})
}

// request 是核心：登记 channel → 写一行 → 等响应 / ctx 取消 / 连接断。
func (c *Client) request(ctx context.Context, method string, params any, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	id := c.newID()
	ch := make(chan rpcMessage, 1) // 带缓冲：读循环删完 pending 后再发也不会阻塞

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.writeLine(rpcMessage{ID: &id, Method: method, Params: raw}); err != nil {
		c.forget(id)
		return err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("%s (code %d)", resp.Error.Message, resp.Error.Code)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(resp.Result, out)

	case <-ctx.Done():
		c.forget(id)
		// 规范里的取消方式是发通知，不是直接断开
		c.notify("notifications/cancelled", map[string]any{"requestId": id, "reason": "ctx cancelled"})
		return ctx.Err()

	case <-c.dead:
		return fmt.Errorf("mcp:%s 连接已断开", c.name)
	}
}

// logStderr 把服务端日志转发出来，加前缀区分是哪个 server。
func (c *Client) logStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		log.Printf("[mcp:%s] %s", c.name, sc.Text())
	}
}

// readLoop 是唯一读 stdout 的 goroutine。
// 三类消息：服务端请求（有 method 有 id）、通知（有 method 无 id）、响应（无 method 有 id）。
func (c *Client) readLoop(r io.Reader) {
	defer close(c.dead) // 进程没了/管道断了，所有等待者立刻醒

	sc := bufio.NewScanner(r)
	// 默认上限 64KB，工具返回大结果会直接报 "token too long"，必须放大
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("[mcp:%s] 无法解析的报文：%s", c.name, string(line))
			continue
		}

		// 1) 服务端发来的请求（比如 roots/list）：我们没有实现，回"不支持"
		//    注意必须先判 method，否则会被当成响应丢掉，对端一直等
		if msg.Method != "" && msg.ID != nil {
			c.respondError(*msg.ID, -32601, "客户端不支持 "+msg.Method)
			continue
		}

		// 2) 通知：不需要回包，先打日志（tools/list_changed 可以在这里做重注册）
		if msg.Method != "" {
			log.Printf("[mcp:%s] 通知：%s", c.name, msg.Method)
			continue
		}

		// 3) 响应：按 id 唤醒等待者
		if msg.ID == nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[*msg.ID]
		delete(c.pending, *msg.ID)
		c.mu.Unlock()

		if ch != nil {
			ch <- msg
		}
	}

	if err := sc.Err(); err != nil {
		log.Printf("[mcp:%s] 读循环结束：%v", c.name, err)
	}
}
