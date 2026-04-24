//go:build integration

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"microagent2/internal/broker"
	"microagent2/internal/llmproxy"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
)

// fakeLlamaNonStream returns a single OpenAI-format chat completion.
func fakeLlamaNonStream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from fake llama"},"finish_reason":"stop"}]}`)
	}))
}

// fakeLlamaStream returns an SSE stream with a few content deltas.
func fakeLlamaStream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`{"choices":[{"delta":{"role":"assistant"}}]}`,
			`{"choices":[{"delta":{"content":"hello "}}]}`,
			`{"choices":[{"delta":{"content":"world"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

func startBroker(t *testing.T, ctx context.Context, client *messaging.Client, llamaAddr string, agentSlots, hindsightSlots int) {
	t.Helper()
	reg := registry.NewRegistry()
	b := broker.NewWithClasses(client, reg, testLogger, llamaAddr, "", "test", agentSlots, hindsightSlots, 5*time.Second, 2*time.Second, 30*time.Second)
	go b.Run(ctx)
	time.Sleep(100 * time.Millisecond) // allow goroutines to start
}

func startLLMProxy(t *testing.T, ctx context.Context, client *messaging.Client) *httptest.Server {
	t.Helper()
	srv := llmproxy.New(client, llmproxy.Config{
		Identity:       "llm-proxy-test",
		SlotTimeout:    5 * time.Second,
		RequestTimeout: 15 * time.Second,
	}, testLogger)
	proxyHTTP := httptest.NewServer(srv.Handler())
	t.Cleanup(proxyHTTP.Close)
	return proxyHTTP
}

func TestLLMProxyNonStreamingHappyPath(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	llama := fakeLlamaNonStream()
	defer llama.Close()
	llamaAddr := strings.TrimPrefix(llama.URL, "http://")

	startBroker(t, ctx, client, llamaAddr, 0, 2)
	proxy := startLLMProxy(t, ctx, client)

	body := `{"model":"test","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, b)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("choices: %+v", out.Choices)
	}
	if out.Choices[0].Message.Content != "hello from fake llama" {
		t.Fatalf("content = %q", out.Choices[0].Message.Content)
	}
	if out.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish = %q", out.Choices[0].FinishReason)
	}
}

func TestLLMProxyStreamingHappyPath(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	llama := fakeLlamaStream()
	defer llama.Close()
	llamaAddr := strings.TrimPrefix(llama.URL, "http://")

	startBroker(t, ctx, client, llamaAddr, 0, 2)
	proxy := startLLMProxy(t, ctx, client)

	body := `{"model":"test","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	var content bytes.Buffer
	scanner := make([]byte, 0, 4096)
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			scanner = append(scanner, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	got := string(scanner)
	if !strings.Contains(got, "data: [DONE]") {
		t.Fatalf("missing [DONE] terminator; got:\n%s", got)
	}

	// Collect all delta.content fragments.
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Choices {
			content.WriteString(c.Delta.Content)
		}
	}
	if content.String() != "hello world" {
		t.Fatalf("streamed content = %q; want %q", content.String(), "hello world")
	}
}

func TestLLMProxySlotTimeoutReturns503(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	llama := fakeLlamaNonStream()
	defer llama.Close()
	llamaAddr := strings.TrimPrefix(llama.URL, "http://")

	// No hindsight slots — request must time out.
	startBroker(t, ctx, client, llamaAddr, 2, 0)

	srv := llmproxy.New(client, llmproxy.Config{
		Identity:       "llm-proxy-test",
		SlotTimeout:    500 * time.Millisecond,
		RequestTimeout: 10 * time.Second,
	}, testLogger)
	proxy := httptest.NewServer(srv.Handler())
	defer proxy.Close()

	body := `{"model":"test","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s; want 503", resp.StatusCode, b)
	}
	var body503 struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body503); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body503.Error.Code != "slot_unavailable" {
		t.Fatalf("error code = %q; want slot_unavailable", body503.Error.Code)
	}
}

func TestLLMProxyHealth(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proxy := startLLMProxy(t, ctx, client)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
