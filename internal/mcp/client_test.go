package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

// fakeServer drives a goroutine that reads JSON-RPC requests line-by-line
// from clientToServer and writes responses to serverToClient. Caller
// provides a handler that produces the response for each request.
func fakeServer(t *testing.T, clientStdin io.Reader, clientStdout io.Writer, handler func(req rpcMessage) (result any, errObj *rpcError, isNotification bool)) {
	t.Helper()
	scanner := bufio.NewScanner(clientStdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcMessage
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		res, errObj, isNotif := handler(req)
		if isNotif {
			continue // notifications have no response
		}
		msg := map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(req.ID),
		}
		if errObj != nil {
			msg["error"] = errObj
		} else {
			if b, err := json.Marshal(res); err == nil {
				msg["result"] = json.RawMessage(b)
			}
		}
		b, _ := json.Marshal(msg)
		_, _ = clientStdout.Write(append(b, '\n'))
	}
}

func newTestClient(t *testing.T, handler func(req rpcMessage) (any, *rpcError, bool)) *Client {
	t.Helper()
	// stdin to server (client writes, server reads)
	cReader, cWriter := io.Pipe()
	// stdout from server (server writes, client reads)
	sReader, sWriter := io.Pipe()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	c := newClientFromPipes("test", cWriter, sReader, nil, logger, 2*time.Second)
	t.Cleanup(func() {
		// Close the server's stdout writer first so the client's read loop
		// sees EOF and exits immediately; otherwise Close waits its full
		// grace period.
		_ = sWriter.Close()
		_ = cReader.Close()
		_ = c.Close()
	})

	go fakeServer(t, cReader, sWriter, handler)
	return c
}

func TestClient_InitializeHandshake(t *testing.T) {
	initReceived := make(chan struct{}, 1)
	initializedNotif := make(chan struct{}, 1)
	c := newTestClient(t, func(req rpcMessage) (any, *rpcError, bool) {
		switch req.Method {
		case "initialize":
			select {
			case initReceived <- struct{}{}:
			default:
			}
			return map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fake"}}, nil, false
		case "notifications/initialized":
			select {
			case initializedNotif <- struct{}{}:
			default:
			}
			return nil, nil, true
		}
		return nil, &rpcError{Code: -32601, Message: "not found"}, false
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	select {
	case <-initReceived:
	case <-time.After(time.Second):
		t.Fatal("initialize not received")
	}
	select {
	case <-initializedNotif:
	case <-time.After(time.Second):
		t.Fatal("notifications/initialized not received")
	}
}

func TestClient_ListTools(t *testing.T) {
	c := newTestClient(t, func(req rpcMessage) (any, *rpcError, bool) {
		if req.Method == "tools/list" {
			return map[string]any{
				"tools": []map[string]any{
					{"name": "echo", "description": "echo input", "inputSchema": map[string]any{"type": "object"}},
				},
			}, nil, false
		}
		return nil, &rpcError{Code: -32601, Message: "not found"}, false
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(got) != 1 || got[0].Name != "echo" || got[0].Description != "echo input" {
		t.Fatalf("got: %+v", got)
	}
}

func TestClient_CallTool_TextOnly(t *testing.T) {
	c := newTestClient(t, func(req rpcMessage) (any, *rpcError, bool) {
		if req.Method == "tools/call" {
			return map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "hello "},
					{"type": "text", "text": "world"},
				},
				"isError": false,
			}, nil, false
		}
		return nil, nil, false
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := c.CallTool(ctx, "echo", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Fatalf("out: %q", out)
	}
}

func TestClient_CallTool_MixedContent(t *testing.T) {
	c := newTestClient(t, func(req rpcMessage) (any, *rpcError, bool) {
		if req.Method == "tools/call" {
			return map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "before"},
					{"type": "image", "data": "..."},
					{"type": "text", "text": "after"},
				},
				"isError": false,
			}, nil, false
		}
		return nil, nil, false
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := c.CallTool(ctx, "x", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "before[non-text content omitted: image]after" {
		t.Fatalf("out: %q", out)
	}
}

func TestClient_CallTool_IsErrorWrapped(t *testing.T) {
	c := newTestClient(t, func(req rpcMessage) (any, *rpcError, bool) {
		if req.Method == "tools/call" {
			return map[string]any{
				"content": []map[string]any{{"type": "text", "text": "bad input"}},
				"isError": true,
			}, nil, false
		}
		return nil, nil, false
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := c.CallTool(ctx, "x", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"error":"bad input"}` {
		t.Fatalf("out: %q", out)
	}
}

func TestClient_CallTool_Timeout(t *testing.T) {
	// Handler that never responds to tools/call.
	c := newTestClient(t, func(req rpcMessage) (any, *rpcError, bool) {
		if req.Method == "tools/call" {
			// Don't respond (simulate hang) — but we can't actually block here
			// without deadlocking fakeServer. Instead return nothing, but the
			// fakeServer still writes a (possibly invalid) response. To
			// simulate a timeout, we skip the write by returning nil-nil-true
			// (treat as notification == no response).
			return nil, nil, true
		}
		return nil, nil, false
	})
	// Override invoke timeout to something short.
	c.invokeTimeout = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, err := c.CallTool(ctx, "x", `{}`)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
