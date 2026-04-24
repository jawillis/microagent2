package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const protocolVersion = "2024-11-05"

// errClientClosed is returned when a request is made on a closed or
// disconnected client.
var errClientClosed = errors.New("mcp client closed")

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type Client struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	logger *slog.Logger

	writeMu sync.Mutex
	callMu  sync.Mutex // serializes tools/call per the design

	nextID  atomic.Int64
	pending sync.Map // map[int64]chan *rpcMessage

	closed   atomic.Bool
	closeErr atomic.Value // error
	doneCh   chan struct{}

	invokeTimeout time.Duration
}

// NewClient spawns the subprocess, attaches pipes, and starts the read loop.
// The caller should then call Initialize, ListTools, and eventually Close.
func NewClient(ctx context.Context, name, command string, args []string, env map[string]string, logger *slog.Logger, invokeTimeout time.Duration) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = mergeEnv(os.Environ(), env)
	attachPlatformAttrs(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	c := newClientFromPipes(name, stdin, stdout, stderr, logger, invokeTimeout)
	c.cmd = cmd
	return c, nil
}

// newClientFromPipes is a test-friendly constructor that wires an existing
// set of pipes (no subprocess) so unit tests can drive a fake server via
// goroutines. Real code paths use NewClient.
func newClientFromPipes(name string, stdin io.WriteCloser, stdout io.ReadCloser, stderr io.ReadCloser, logger *slog.Logger, invokeTimeout time.Duration) *Client {
	c := &Client{
		name:          name,
		stdin:         stdin,
		logger:        logger,
		doneCh:        make(chan struct{}),
		invokeTimeout: invokeTimeout,
	}
	go c.readLoop(stdout)
	if stderr != nil {
		go c.stderrLogger(stderr)
	}
	return c
}

func (c *Client) readLoop(stdout io.ReadCloser) {
	defer close(c.doneCh)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.logger.Warn("mcp_read_parse_failed", "server", c.name, "error", err.Error())
			continue
		}
		if len(msg.ID) == 0 {
			// Notification — drain.
			continue
		}
		id, err := parseID(msg.ID)
		if err != nil {
			c.logger.Warn("mcp_read_bad_id", "server", c.name, "id", string(msg.ID))
			continue
		}
		chAny, ok := c.pending.LoadAndDelete(id)
		if !ok {
			c.logger.Warn("mcp_read_orphan_response", "server", c.name, "id", id)
			continue
		}
		ch := chAny.(chan *rpcMessage)
		// Non-blocking send — buffered channel of size 1.
		select {
		case ch <- &msg:
		default:
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.closed.Store(true)
	c.closeErr.Store(err)
	// Close all pending channels so waiters unblock.
	c.pending.Range(func(k, v any) bool {
		c.pending.Delete(k)
		return true
	})
	c.logger.Info("mcp_server_disconnected", "server", c.name, "reason", err.Error())
}

func (c *Client) stderrLogger(stderr io.ReadCloser) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 32*1024), 1024*1024)
	for scanner.Scan() {
		c.logger.Info("mcp_server_stderr", "server", c.name, "line", scanner.Text())
	}
}

func parseID(raw json.RawMessage) (int64, error) {
	s := string(raw)
	if len(s) > 0 && s[0] == '"' {
		s = s[1 : len(s)-1]
	}
	return strconv.ParseInt(s, 10, 64)
}

func mergeEnv(base []string, extra map[string]string) []string {
	out := make([]string, len(base), len(base)+len(extra))
	copy(out, base)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// Name returns the server's configured name.
func (c *Client) Name() string { return c.name }

// call sends a request and waits for the response or context cancellation.
func (c *Client) call(ctx context.Context, method string, params any) (*rpcMessage, error) {
	if c.closed.Load() {
		return nil, errClientClosed
	}
	id := c.nextID.Add(1)
	ch := make(chan *rpcMessage, 1)
	c.pending.Store(id, ch)

	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			c.pending.Delete(id)
			return nil, err
		}
		paramsRaw = b
	}
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if len(paramsRaw) > 0 {
		req["params"] = paramsRaw
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		c.pending.Delete(id)
		return nil, err
	}
	reqBytes = append(reqBytes, '\n')

	c.writeMu.Lock()
	_, err = c.stdin.Write(reqBytes)
	c.writeMu.Unlock()
	if err != nil {
		c.pending.Delete(id)
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok || resp == nil {
			return nil, errClientClosed
		}
		return resp, nil
	case <-ctx.Done():
		c.pending.Delete(id)
		return nil, ctx.Err()
	case <-c.doneCh:
		return nil, errClientClosed
	}
}

func (c *Client) notify(method string, params any) error {
	if c.closed.Load() {
		return errClientClosed
	}
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	_, err = c.stdin.Write(b)
	c.writeMu.Unlock()
	return err
}

// Initialize performs the MCP initialize handshake.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "microagent2", "version": "0.1.0"},
	}
	resp, err := c.call(ctx, "initialize", params)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}
	c.logger.Info("mcp_initialize_ok", "server", c.name)
	return nil
}

// ListTools returns the tools advertised by the server.
func (c *Client) ListTools(ctx context.Context) ([]MCPTool, error) {
	resp, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}
	var out struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return out.Tools, nil
}

type callToolResponse struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

type contentBlock struct {
	Type string          `json:"type"`
	Text string          `json:"text,omitempty"`
	Raw  json.RawMessage `json:"-"`
}

// CallTool invokes a tool by its original (non-namespaced) name with argsJSON.
// Returns the flattened content string; the caller is responsible for
// surfacing isError via the wrapper's convention.
func (c *Client) CallTool(ctx context.Context, toolName, argsJSON string) (string, error) {
	c.callMu.Lock()
	defer c.callMu.Unlock()

	if c.closed.Load() {
		return "", errClientClosed
	}

	var args map[string]any
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("parse arguments: %w", err)
		}
	}

	deadline := c.invokeTimeout
	if deadline <= 0 {
		deadline = 30 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	resp, err := c.call(callCtx, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("tools/call error: %s", resp.Error.Message)
	}
	var payload callToolResponse
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		return "", fmt.Errorf("decode tools/call: %w", err)
	}
	flat := flattenContent(payload.Content)
	if payload.IsError {
		b, _ := json.Marshal(map[string]string{"error": flat})
		return string(b), nil
	}
	return flat, nil
}

func flattenContent(blocks []contentBlock) string {
	var b []byte
	for _, blk := range blocks {
		if blk.Type == "text" {
			b = append(b, blk.Text...)
			continue
		}
		b = append(b, "[non-text content omitted: "...)
		b = append(b, blk.Type...)
		b = append(b, ']')
	}
	return string(b)
}

// Close signals the subprocess to exit. Waits up to 2s, then kills.
func (c *Client) Close() error {
	_ = c.stdin.Close()
	if c.cmd == nil {
		// pipe-based test client: just wait for read loop to finish
		select {
		case <-c.doneCh:
		case <-time.After(1 * time.Second):
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		killProcessGroup(c.cmd)
		<-done
		return nil
	}
}

// Disconnected reports whether the subprocess has exited.
func (c *Client) Disconnected() bool {
	return c.closed.Load()
}
