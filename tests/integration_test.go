//go:build integration

package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"microagent2/internal/agent"
	"microagent2/internal/broker"
	"microagent2/internal/config"
	appcontext "microagent2/internal/context"
	"microagent2/internal/gateway"
	"microagent2/internal/memoryclient"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
	"microagent2/internal/response"
	"microagent2/internal/retro"
)

// newFakeMemoryService returns an httptest server that speaks the memory-service
// API. memories (optional) pre-populate the /recall response.
func newFakeMemoryService(memories []memoryclient.MemorySummary) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"status":"ok","hindsight":"reachable","bank":"microagent2"}`)
		case "/recall":
			resp := memoryclient.RecallResponse{Memories: memories}
			_ = json.NewEncoder(w).Encode(resp)
		case "/retain":
			_ = json.NewEncoder(w).Encode(memoryclient.RetainResponse{Success: true, BankID: "microagent2", ItemsCount: 1})
		case "/reflect":
			_ = json.NewEncoder(w).Encode(memoryclient.ReflectResponse{Text: ""})
		case "/forget":
			_ = json.NewEncoder(w).Encode(memoryclient.ForgetResponse{DeletedID: ""})
		default:
			http.NotFound(w, r)
		}
	}))
}

var testLogger = slog.New(slog.NewJSONHandler(io.Discard, nil))

func newTestClient(t *testing.T) *messaging.Client {
	t.Helper()
	addr := "localhost:6379"
	client := messaging.NewClient(addr)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		t.Skipf("Valkey not available at %s: %v", addr, err)
	}
	client.Redis().FlushDB(ctx)
	t.Cleanup(func() { client.Close() })
	return client
}

// 10.1 End-to-end: client → gateway → context manager → main agent → broker → llama-server → response
func TestEndToEnd(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start a mock llama-server that returns an OpenAI-format completion
	llamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello from llama"},"finish_reason":"stop"}]}`)
	}))
	defer llamaServer.Close()
	llamaAddr := strings.TrimPrefix(llamaServer.URL, "http://")

	// Start a fake memory-service that returns no memories.
	memSrv := newFakeMemoryService(nil)
	defer memSrv.Close()

	// Set up broker
	reg := registry.NewRegistry()
	b := broker.New(client, reg, testLogger, llamaAddr, "", "test", 4, 5*time.Second, 2*time.Second, 30*time.Second)
	go b.Run(ctx)

	// Set up context manager
	respStore := response.NewStore(client.Redis())
	mc := memoryclient.New(memSrv.URL)
	assembler := appcontext.NewAssembler("You are a test assistant.")
	cfgStore := config.NewStore(client.Redis())
	mgr := appcontext.NewManager(client, respStore, mc, assembler, testLogger, 5, 3, cfgStore)
	go mgr.Run(ctx)

	// Register main agent
	agentReg := registry.NewAgentRegistrar(client, messaging.RegisterPayload{
		AgentID:             "main-agent",
		Priority:            0,
		Preemptible:         false,
		Capabilities:        []string{"chat"},
		Trigger:             "request-driven",
		HeartbeatIntervalMS: 3000,
	})
	if err := agentReg.Register(ctx); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	go agentReg.RunHeartbeat(ctx)

	// Give broker time to process registration
	time.Sleep(500 * time.Millisecond)

	// Set up main agent runtime consuming requests
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
					continue
				}

				_, slotErr := rt.RequestSlot(ctx)
				if slotErr != nil {
					continue
				}

				result, _, _ := rt.Execute(ctx, payload.Messages, nil, nil, nil)
				_ = rt.ReleaseSlot(ctx)

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

	// Send request through gateway
	responses := response.NewStore(client.Redis())
	gw := gateway.New(client, testLogger, nil, responses, 120, "8080", "http://localhost:8081", "http://localhost:8100")
	reqBody := `{"model":"test","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	gw.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	choices, ok := resp["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("expected choices in response: %s", w.Body.String())
	}

	t.Logf("end-to-end response: %s", w.Body.String())
}

// 10.2 Correlation ID propagation
func TestCorrelationIDPropagation(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	correlationID := messaging.NewCorrelationID()

	msg, err := messaging.NewMessage(messaging.TypeChatRequest, "test", messaging.ChatRequestPayload{
		SessionID: "test-session",
		Messages:  []messaging.ChatMsg{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}
	msg.CorrelationID = correlationID

	replyStream := "stream:test:reply:" + correlationID
	msg.ReplyStream = replyStream

	// Publish and read back to verify correlation ID survives serialization
	stream := "stream:test:correlation"
	group := "cg:test:correlation"

	if _, err := client.Publish(ctx, stream, msg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := client.EnsureGroup(ctx, stream, group); err != nil {
		t.Fatalf("ensure group: %v", err)
	}

	msgs, ids, err := client.ReadGroup(ctx, stream, group, "reader", 1, 5*time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("no messages read")
	}

	if msgs[0].CorrelationID != correlationID {
		t.Errorf("correlation ID mismatch: got %s, want %s", msgs[0].CorrelationID, correlationID)
	}
	if msgs[0].ReplyStream != replyStream {
		t.Errorf("reply stream mismatch: got %s, want %s", msgs[0].ReplyStream, replyStream)
	}

	// Verify reply preserves correlation ID
	reply, err := messaging.NewReply(msgs[0], messaging.TypeChatResponse, "test-responder", messaging.ChatResponsePayload{
		Content: "reply",
		Done:    true,
	})
	if err != nil {
		t.Fatalf("create reply: %v", err)
	}
	if reply.CorrelationID != correlationID {
		t.Errorf("reply correlation ID mismatch: got %s, want %s", reply.CorrelationID, correlationID)
	}

	_ = client.Ack(ctx, stream, group, ids[0])
}

// 10.3 Preemption flow
func TestPreemptionFlow(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	reg := registry.NewRegistry()
	reg.Register(&registry.AgentInfo{
		AgentID:     "retro-test",
		Priority:    1,
		Preemptible: true,
	})

	// 2-slot broker, both initially unassigned
	llamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // slow response to keep slot occupied
		fmt.Fprint(w, `{"content":"done","stop":true}`)
	}))
	defer llamaServer.Close()

	b := broker.New(client, reg, testLogger, strings.TrimPrefix(llamaServer.URL, "http://"), "", "test", 2, 2*time.Second, 1*time.Second, 30*time.Second)
	go b.Run(ctx)

	time.Sleep(300 * time.Millisecond)

	// Retro agent takes slot 1
	retroRT := agent.NewRuntime(client, "retro-test", 1, true, testLogger)
	slotID, err := retroRT.RequestSlot(ctx)
	if err != nil {
		t.Fatalf("retro agent failed to get slot: %v", err)
	}
	t.Logf("retro agent got slot %d", slotID)

	// Listen for preemption on retro agent
	preempted := make(chan struct{})
	preemptCtx, preemptCancel := context.WithCancel(ctx)
	defer preemptCancel()
	go func() {
		retroRT.ListenForPreemption(preemptCtx)
		close(preempted)
	}()

	// Allow pub/sub subscription to establish before triggering preemption
	time.Sleep(200 * time.Millisecond)

	// Higher priority agent (priority 0) requests a slot — all taken, should trigger preemption
	mainRT := agent.NewRuntime(client, "urgent-agent", 0, false, testLogger)
	reg.Register(&registry.AgentInfo{
		AgentID:     "urgent-agent",
		Priority:    0,
		Preemptible: false,
	})

	go func() {
		newSlot, err := mainRT.RequestSlot(ctx)
		if err != nil {
			t.Logf("urgent agent slot request result: %v", err)
			return
		}
		t.Logf("urgent agent got slot %d", newSlot)
	}()

	// Wait for preemption signal
	select {
	case <-preempted:
		t.Log("retro agent preempted successfully")
	case <-time.After(10 * time.Second):
		t.Error("preemption did not occur within timeout")
	}
}

// 10.4 Agent registration, heartbeat, and dead-agent recovery
func TestAgentRegistrationAndHeartbeat(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	reg := registry.NewRegistry()

	// Register an agent
	agentReg := registry.NewAgentRegistrar(client, messaging.RegisterPayload{
		AgentID:             "test-agent",
		Priority:            1,
		Preemptible:         true,
		Capabilities:        []string{"test"},
		Trigger:             "event-driven",
		HeartbeatIntervalMS: 500,
	})
	if err := agentReg.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Start registration consumer
	consumer := registry.NewRegistryConsumer(client, reg, testLogger, func(agentID string) {
		t.Logf("dead agent callback: %s", agentID)
	})
	go consumer.RunRegistrationConsumer(ctx)
	go consumer.RunHeartbeatMonitor(ctx)

	// Start heartbeat
	hbCtx, hbCancel := context.WithCancel(ctx)
	go agentReg.RunHeartbeat(hbCtx)

	// Give time for registration to process
	time.Sleep(time.Second)

	info, ok := reg.Get("test-agent")
	if !ok {
		t.Fatal("agent not found in registry after registration")
	}
	if !info.Alive {
		t.Error("agent should be alive after registration")
	}
	if info.Priority != 1 {
		t.Errorf("expected priority 1, got %d", info.Priority)
	}

	// Stop heartbeat, wait for agent to be marked dead
	// Heartbeat monitor ticker is 5s, then 3x500ms=1.5s miss threshold
	hbCancel()
	time.Sleep(8 * time.Second)

	info, ok = reg.Get("test-agent")
	if !ok {
		t.Fatal("agent should still be in registry")
	}
	if info.Alive {
		t.Error("agent should be marked dead after heartbeat stops")
	}

	// Test deregistration
	if err := agentReg.Deregister(ctx); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	time.Sleep(time.Second)

	_, ok = reg.Get("test-agent")
	if ok {
		t.Error("agent should be removed after deregistration")
	}
}

// 10.5 Memory injection
func TestMemoryInjection(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Fake memory-service pre-seeded with a single recall result.
	memSrv := newFakeMemoryService([]memoryclient.MemorySummary{
		{ID: "m1", Content: "User prefers dark mode", Score: 0.92, Tags: []string{"preferences"}},
	})
	defer memSrv.Close()

	respStore := response.NewStore(client.Redis())
	mc := memoryclient.New(memSrv.URL)
	assembler := appcontext.NewAssembler("You are a test assistant.")

	// Store session history via response objects
	sessionID := "test-memory-session"
	r := &response.Response{
		ID:        "resp_mem_test",
		SessionID: sessionID,
		Input:     []response.InputItem{{Type: "message", Role: "user", Content: "I prefer dark mode"}},
		Output:    []response.OutputItem{{Type: "message", Role: "assistant", Content: []response.ContentPart{{Type: "output_text", Text: "Noted."}}}},
		Model:     "test",
		CreatedAt: "2025-01-01T00:00:00Z",
		Status:    response.StatusCompleted,
	}
	if err := respStore.Save(ctx, r); err != nil {
		t.Fatalf("save response: %v", err)
	}

	// Assemble context with memory recall
	recall, err := mc.Recall(ctx, memoryclient.RecallRequest{Query: "dark mode", Limit: 5})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(recall.Memories) == 0 {
		t.Fatal("expected memories from fake memory-service")
	}
	memories := make([]appcontext.Memory, 0, len(recall.Memories))
	for _, m := range recall.Memories {
		memories = append(memories, appcontext.Memory{ID: m.ID, Content: m.Content, Score: m.Score, Tags: m.Tags})
	}

	history, err := respStore.GetSessionMessages(ctx, sessionID)
	if err != nil {
		t.Fatalf("get history: %v", err)
	}

	assembled := assembler.Assemble(memories, history, messaging.ChatMsg{Role: "user", Content: "What theme should I use?"})

	// Verify memory appears in assembled prompt
	foundMemory := false
	for _, msg := range assembled {
		if strings.Contains(msg.Content, "dark mode") {
			foundMemory = true
			break
		}
	}
	if !foundMemory {
		t.Error("memory not found in assembled prompt")
		for _, msg := range assembled {
			t.Logf("[%s]: %s", msg.Role, msg.Content[:min(len(msg.Content), 100)])
		}
	}
}

// 10.6 Retro agent triggers
func TestRetroAgentTriggers(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("inactivity_trigger", func(t *testing.T) {
		activated := make(chan string, 1)

		trigger := retro.NewTrigger(client, testLogger, 2*time.Second, func(sessionID string) {
			activated <- sessionID
		})

		go trigger.RunInactivityTrigger(ctx)
		time.Sleep(200 * time.Millisecond)

		// Publish a session event
		eventMsg, _ := messaging.NewMessage(messaging.TypeSessionEvent, "test", messaging.SessionEventPayload{
			SessionID: "inactivity-test-session",
			Event:     "message_received",
		})
		_ = client.PubSubPublish(ctx, messaging.ChannelEvents, eventMsg)

		// Wait for inactivity timeout
		select {
		case sid := <-activated:
			if sid != "inactivity-test-session" {
				t.Errorf("wrong session activated: %s", sid)
			}
			t.Log("inactivity trigger fired correctly")
		case <-time.After(5 * time.Second):
			t.Error("inactivity trigger did not fire")
		}
	})

	t.Run("session_end_trigger", func(t *testing.T) {
		activated := make(chan string, 1)

		trigger := retro.NewTrigger(client, testLogger, 5*time.Minute, func(sessionID string) {
			activated <- sessionID
		})

		go trigger.RunSessionEndTrigger(ctx)
		time.Sleep(200 * time.Millisecond)

		// Publish session_ended event
		eventMsg, _ := messaging.NewMessage(messaging.TypeSessionEvent, "test", messaging.SessionEventPayload{
			SessionID: "end-test-session",
			Event:     "session_ended",
		})
		_ = client.PubSubPublish(ctx, messaging.ChannelEvents, eventMsg)

		select {
		case sid := <-activated:
			if sid != "end-test-session" {
				t.Errorf("wrong session activated: %s", sid)
			}
			t.Log("session end trigger fired correctly")
		case <-time.After(5 * time.Second):
			t.Error("session end trigger did not fire")
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 10.7 Two consecutive /v1/responses turns both reach the broker (regression for slot-leak hang)
func TestTwoTurnResponsesNoSlotLeak(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var llamaCalls int32
	llamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&llamaCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"reply"},"finish_reason":"stop"}]}`)
	}))
	defer llamaServer.Close()
	llamaAddr := strings.TrimPrefix(llamaServer.URL, "http://")

	memSrv := newFakeMemoryService(nil)
	defer memSrv.Close()

	reg := registry.NewRegistry()
	b := broker.New(client, reg, testLogger, llamaAddr, "", "test", 2, 5*time.Second, 2*time.Second, 30*time.Second)
	go b.Run(ctx)

	respStore := response.NewStore(client.Redis())
	mc := memoryclient.New(memSrv.URL)
	assembler := appcontext.NewAssembler("You are a test assistant.")
	cfgStore := config.NewStore(client.Redis())
	mgr := appcontext.NewManager(client, respStore, mc, assembler, testLogger, 5, 3, cfgStore)
	go mgr.Run(ctx)

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

	gw := gateway.New(client, testLogger, nil, respStore, 15, "8080", "http://"+llamaAddr, memSrv.URL)

	send := func(body string) map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	turn1 := send(`{"model":"test","input":"first"}`)
	respID, _ := turn1["id"].(string)
	if respID == "" {
		t.Fatalf("turn 1 missing id: %v", turn1)
	}

	send(fmt.Sprintf(`{"model":"test","input":"second","previous_response_id":%q}`, respID))

	if got := atomic.LoadInt32(&llamaCalls); got != 2 {
		t.Fatalf("expected 2 llama calls, got %d (slot leak regression)", got)
	}
}

// 10.8 Broker reclaims a provisional slot when the requester never acks.
func TestBrokerReclaimsProvisionalOnAckTimeout(t *testing.T) {
	client := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	reg := registry.NewRegistry()
	b := broker.New(client, reg, testLogger, "127.0.0.1:0", "", "test", 1, 5*time.Second, 500*time.Millisecond, 30*time.Second)
	go b.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	replyStream := fmt.Sprintf("stream:test:reply:%d", time.Now().UnixNano())
	reqMsg, err := messaging.NewMessage(messaging.TypeSlotRequest, "ghost-agent", messaging.SlotRequestPayload{AgentID: "ghost-agent", Priority: 0})
	if err != nil {
		t.Fatalf("new message: %v", err)
	}
	reqMsg.ReplyStream = replyStream

	if _, err := client.Publish(ctx, "stream:broker:slot-requests", reqMsg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Drain the provisional reply to the void (simulating a client that got the reply but never acked).
	group := "cg:test:reclaim"
	if err := client.EnsureGroup(ctx, replyStream, group); err != nil {
		t.Fatalf("ensure group: %v", err)
	}
	msgs, ids, _ := client.ReadGroup(ctx, replyStream, group, "r", 1, 3*time.Second)
	if len(msgs) == 0 {
		t.Fatal("did not receive slot-assigned reply")
	}
	for _, id := range ids {
		_ = client.Ack(ctx, replyStream, group, id)
	}

	// Wait past provisional timeout so the broker reclaims.
	time.Sleep(1 * time.Second)

	// A fresh requester should now be able to claim the slot.
	reply2Stream := fmt.Sprintf("stream:test:reply2:%d", time.Now().UnixNano())
	req2, _ := messaging.NewMessage(messaging.TypeSlotRequest, "real-agent", messaging.SlotRequestPayload{AgentID: "real-agent", Priority: 0})
	req2.ReplyStream = reply2Stream
	if _, err := client.Publish(ctx, "stream:broker:slot-requests", req2); err != nil {
		t.Fatalf("publish 2: %v", err)
	}
	group2 := "cg:test:reclaim2"
	if err := client.EnsureGroup(ctx, reply2Stream, group2); err != nil {
		t.Fatalf("ensure group 2: %v", err)
	}
	msgs2, _, _ := client.ReadGroup(ctx, reply2Stream, group2, "r", 1, 3*time.Second)
	if len(msgs2) == 0 {
		t.Fatal("second requester did not get slot — reclaim did not fire")
	}
}
