//go:build integration

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"microagent2/internal/agent"
	"microagent2/internal/broker"
	"microagent2/internal/config"
	appcontext "microagent2/internal/context"
	"microagent2/internal/gateway"
	"microagent2/internal/hindsight"
	"microagent2/internal/memoryclient"
	"microagent2/internal/memoryservice"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
	"microagent2/internal/response"
)

type capturedRetain struct {
	BankID string
	Body   hindsight.RetainRequest
}

type capturedRecall struct {
	BankID string
	Body   hindsight.RecallRequest
}

type fakeHindsight struct {
	mu       sync.Mutex
	retains  []capturedRetain
	recalls  []capturedRecall
	memories []hindsight.RecallResult
	srv      *httptest.Server
}

func newFakeHindsight(memories []hindsight.RecallResult) *fakeHindsight {
	fh := &fakeHindsight{memories: memories}
	fh.srv = httptest.NewServer(http.HandlerFunc(fh.handler))
	return fh
}

func (fh *fakeHindsight) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if strings.HasSuffix(r.URL.Path, "/memories/recall") && r.Method == http.MethodPost {
		var body hindsight.RecallRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		bankID := extractBankIDFromPath(r.URL.Path, "/memories/recall")
		fh.mu.Lock()
		fh.recalls = append(fh.recalls, capturedRecall{BankID: bankID, Body: body})
		results := fh.memories
		fh.mu.Unlock()
		json.NewEncoder(w).Encode(hindsight.RecallResponse{Results: results})
		return
	}

	if strings.HasSuffix(r.URL.Path, "/memories") && r.Method == http.MethodPost {
		var body hindsight.RetainRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		bankID := extractBankIDFromPath(r.URL.Path, "/memories")
		fh.mu.Lock()
		fh.retains = append(fh.retains, capturedRetain{BankID: bankID, Body: body})
		fh.mu.Unlock()
		json.NewEncoder(w).Encode(hindsight.RetainResponse{Success: true, BankID: bankID, ItemsCount: len(body.Items)})
		return
	}

	if strings.Contains(r.URL.Path, "/banks") && r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(map[string]any{"banks": []any{}})
		return
	}

	http.NotFound(w, r)
}

func extractBankIDFromPath(path, suffix string) string {
	path = strings.TrimSuffix(path, suffix)
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

func (fh *fakeHindsight) getRetains() []capturedRetain {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	out := make([]capturedRetain, len(fh.retains))
	copy(out, fh.retains)
	return out
}

func (fh *fakeHindsight) getRecalls() []capturedRecall {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	out := make([]capturedRecall, len(fh.recalls))
	copy(out, fh.recalls)
	return out
}

func (fh *fakeHindsight) close() { fh.srv.Close() }

type identityHarness struct {
	gw         *gateway.Server
	cfgStore   *config.Store
	fh         *fakeHindsight
	memSrvURL  string
	cleanup    func()
}

func newIdentityHarness(t *testing.T, memories []hindsight.RecallResult) *identityHarness {
	t.Helper()
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	fh := newFakeHindsight(memories)

	llamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"reply"},"finish_reason":"stop"}]}`)
	}))
	llamaAddr := strings.TrimPrefix(llamaServer.URL, "http://")

	hc := hindsight.New(fh.srv.URL, "")
	cfgStore := config.NewStore(client.Redis())
	memSrv := memoryservice.New(hc, memoryservice.Config{
		BankID:      "test-bank",
		ExternalURL: "http://localhost:0",
		Resolver: func(ctx context.Context) config.MemoryConfig {
			return config.ResolveMemory(ctx, cfgStore)
		},
	}, testLogger)
	memHTTP := httptest.NewServer(memSrv.Handler())

	respStore := response.NewStore(client.Redis())
	mc := memoryclient.New(memHTTP.URL)
	assembler := appcontext.NewAssembler("You are a test assistant.")
	mgr := appcontext.NewManager(client, respStore, mc, assembler, testLogger, 5, 3, cfgStore)
	go mgr.Run(ctx)

	reg := registry.NewRegistry()
	b := broker.New(client, reg, testLogger, llamaAddr, "", "test", 2, 5*time.Second, 2*time.Second, 30*time.Second)
	go b.Run(ctx)

	agentReg := registry.NewAgentRegistrar(client, messaging.RegisterPayload{
		AgentID:             "main-agent",
		Priority:            0,
		Preemptible:         false,
		Capabilities:        []string{"chat"},
		Trigger:             "request-driven",
		HeartbeatIntervalMS: 3000,
	})
	if err := agentReg.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	go agentReg.RunHeartbeat(ctx)
	time.Sleep(500 * time.Millisecond)

	rt := agent.NewRuntime(client, "main-agent", 0, false, testLogger)
	agentStream := fmt.Sprintf(messaging.StreamAgentRequests, "main-agent")
	agentGroup := fmt.Sprintf(messaging.ConsumerGroupAgent, "main-agent")
	if err := client.EnsureGroup(ctx, agentStream, agentGroup); err != nil {
		t.Fatalf("ensure agent group: %v", err)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			msgs, ids, err := client.ReadGroup(ctx, agentStream, agentGroup, "worker", 1, time.Second)
			if err != nil {
				continue
			}
			for i, msg := range msgs {
				var payload messaging.ContextAssembledPayload
				if err := msg.DecodePayload(&payload); err != nil {
					_ = client.Ack(ctx, agentStream, agentGroup, ids[i])
					continue
				}
				_, slotErr := rt.RequestSlotWithCorrelation(ctx, msg.CorrelationID)
				if slotErr != nil {
					_ = client.Ack(ctx, agentStream, agentGroup, ids[i])
					continue
				}
				result, _, _ := rt.ExecuteWithCorrelation(ctx, msg.CorrelationID, payload.Messages, nil, nil, nil)
				_ = rt.ReleaseSlotWithCorrelation(ctx, msg.CorrelationID)

				if payload.ReplyStream != "" {
					reply, _ := messaging.NewReply(msg, messaging.TypeChatResponse, "main-agent", messaging.ChatResponsePayload{
						SessionID: payload.SessionID,
						Content:   result,
						Done:      true,
					})
					_, _ = client.Publish(ctx, payload.ReplyStream, reply)
				}
				_ = client.Ack(ctx, agentStream, agentGroup, ids[i])
			}
		}
	}()

	gw := gateway.New(client, testLogger, cfgStore, respStore, 15, "8080", "http://"+llamaAddr, memHTTP.URL)

	return &identityHarness{
		gw:        gw,
		cfgStore:  cfgStore,
		fh:        fh,
		memSrvURL: memHTTP.URL,
		cleanup: func() {
			cancel()
			fh.close()
			llamaServer.Close()
			memHTTP.Close()
		},
	}
}

func (h *identityHarness) sendResponses(t *testing.T, ctx context.Context, body string) (int, http.Header, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.gw.ServeHTTP(w, req)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, w.Result().Header, resp
}

// 12.1 Explicit speaker_id="alice" → retained observation carries metadata.speaker_id="alice" in Hindsight.
func TestIdentity_ExplicitSpeakerRetain(t *testing.T) {
	h := newIdentityHarness(t, nil)
	defer h.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Verify gateway resolves speaker and sets X-Speaker-ID header.
	code, respHeader, _ := h.sendResponses(t, ctx, `{"model":"test","input":"I love hiking","speaker_id":"alice"}`)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if got := respHeader.Get("X-Speaker-ID"); got != "alice" {
		t.Fatalf("X-Speaker-ID = %q; want alice", got)
	}

	// Now test the retain path directly: POST to memory-service /retain with
	// explicit speaker_id and verify it reaches Hindsight with that metadata.
	retainBody, _ := json.Marshal(memoryclient.RetainRequest{
		Content:  "Alice loves hiking in the mountains",
		Metadata: map[string]string{"speaker_id": "alice"},
		Entities: []string{"alice"},
		Tags:     []string{"preferences"},
	})
	retainReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.memSrvURL+"/retain", bytes.NewReader(retainBody))
	retainReq.Header.Set("Content-Type", "application/json")
	retainResp, err := http.DefaultClient.Do(retainReq)
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	retainResp.Body.Close()
	if retainResp.StatusCode != http.StatusOK {
		t.Fatalf("retain status = %d; want 200", retainResp.StatusCode)
	}

	retains := h.fh.getRetains()
	if len(retains) == 0 {
		t.Fatal("expected at least one retain in fake Hindsight")
	}
	last := retains[len(retains)-1]
	if len(last.Body.Items) == 0 {
		t.Fatal("no items in retain request")
	}
	item := last.Body.Items[0]
	if item.Metadata["speaker_id"] != "alice" {
		t.Fatalf("Hindsight retain metadata.speaker_id = %q; want alice", item.Metadata["speaker_id"])
	}
	if len(item.Entities) != 1 || item.Entities[0].Text != "alice" {
		t.Fatalf("Hindsight retain entities = %+v; want [{Text:alice}]", item.Entities)
	}
}

// 12.2 Recall with speaker_id="alice" returns Alice's facts; excludes others.
func TestIdentity_RecallSpeakerFilter(t *testing.T) {
	aliceMemory := hindsight.RecallResult{
		ID:       "m-alice-1",
		Text:     "Alice likes green tea",
		Metadata: map[string]string{"speaker_id": "alice", "fact_type": "person_fact"},
		Score:    0.95,
	}
	h := newIdentityHarness(t, []hindsight.RecallResult{aliceMemory})
	defer h.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Set scope=primary + primary_user_id=alice so context-manager's recall
	// adds a speaker_id metadata filter.
	memCfgJSON, _ := json.Marshal(config.MemoryConfig{
		RecallDefaultSpeakerScope: "primary",
		PrimaryUserID:             "alice",
	})
	h.cfgStore.Save(ctx, "memory", memCfgJSON)

	code, _, _ := h.sendResponses(t, ctx, `{"model":"test","input":"what tea does alice like?","speaker_id":"alice"}`)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}

	// Allow async processing.
	time.Sleep(500 * time.Millisecond)

	recalls := h.fh.getRecalls()
	if len(recalls) == 0 {
		t.Fatal("expected at least one recall to fake Hindsight")
	}
	lastRecall := recalls[len(recalls)-1]
	if lastRecall.Body.Metadata == nil || lastRecall.Body.Metadata["speaker_id"] != "alice" {
		t.Fatalf("recall metadata filter: got %v; want speaker_id=alice", lastRecall.Body.Metadata)
	}
}

// 12.3 No speaker_id + no primary_user_id → unknown speaker; X-Speaker-ID: unknown.
func TestIdentity_UnknownSpeaker(t *testing.T) {
	h := newIdentityHarness(t, nil)
	defer h.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	code, respHeader, _ := h.sendResponses(t, ctx, `{"model":"test","input":"hello"}`)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if got := respHeader.Get("X-Speaker-ID"); got != "unknown" {
		t.Fatalf("X-Speaker-ID = %q; want unknown", got)
	}

	// Also verify that a retain with no speaker_id gets defaulted to "unknown"
	// in the memory-service.
	retainBody, _ := json.Marshal(memoryclient.RetainRequest{
		Content: "some world fact",
		Tags:    []string{"knowledge"},
	})
	retainReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.memSrvURL+"/retain", bytes.NewReader(retainBody))
	retainReq.Header.Set("Content-Type", "application/json")
	retainResp, err := http.DefaultClient.Do(retainReq)
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	retainResp.Body.Close()

	retains := h.fh.getRetains()
	if len(retains) == 0 {
		t.Fatal("expected retain in fake Hindsight")
	}
	last := retains[len(retains)-1]
	if len(last.Body.Items) == 0 {
		t.Fatal("no items in retain")
	}
	if last.Body.Items[0].Metadata["speaker_id"] != "unknown" {
		t.Fatalf("speaker_id = %q; want unknown", last.Body.Items[0].Metadata["speaker_id"])
	}
}

// 12.4 Third-party attribution: "Jason: Alice likes green tea" → speaker_id=jason,
// entities=[alice,...], fact_type=person_fact.
func TestIdentity_ThirdPartyAttribution(t *testing.T) {
	h := newIdentityHarness(t, nil)
	defer h.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Gateway resolves speaker_id=jason from the request body.
	code, respHeader, _ := h.sendResponses(t, ctx, `{"model":"test","input":"Alice likes green tea","speaker_id":"jason"}`)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if got := respHeader.Get("X-Speaker-ID"); got != "jason" {
		t.Fatalf("X-Speaker-ID = %q; want jason", got)
	}

	// Simulate what the retro-agent sends to memory-service: third-party attribution
	// where jason reports a fact about alice.
	retainBody, _ := json.Marshal(memoryclient.RetainRequest{
		Content:  "Alice likes green tea",
		Metadata: map[string]string{"speaker_id": "jason", "fact_type": "person_fact"},
		Entities: []string{"alice"},
		Tags:     []string{"preferences"},
	})
	retainReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.memSrvURL+"/retain", bytes.NewReader(retainBody))
	retainReq.Header.Set("Content-Type", "application/json")
	retainResp, err := http.DefaultClient.Do(retainReq)
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	retainResp.Body.Close()
	if retainResp.StatusCode != http.StatusOK {
		t.Fatalf("retain status = %d; want 200", retainResp.StatusCode)
	}

	retains := h.fh.getRetains()
	if len(retains) == 0 {
		t.Fatal("expected retain")
	}
	last := retains[len(retains)-1]
	if len(last.Body.Items) == 0 {
		t.Fatal("no items")
	}
	item := last.Body.Items[0]
	if item.Metadata["speaker_id"] != "jason" {
		t.Fatalf("speaker_id = %q; want jason", item.Metadata["speaker_id"])
	}
	if item.Metadata["fact_type"] != "person_fact" {
		t.Fatalf("fact_type = %q; want person_fact", item.Metadata["fact_type"])
	}
	if len(item.Entities) != 1 || item.Entities[0].Text != "alice" {
		t.Fatalf("entities = %+v; want [{Text:alice}]", item.Entities)
	}
}
