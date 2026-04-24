package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"microagent2/internal/config"
	"microagent2/internal/messaging"
	"microagent2/internal/response"
)

func newTestServer(t *testing.T) (*Server, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	cfgStore := config.NewStore(rdb)
	responses := response.NewStore(rdb)
	client := messaging.NewClient(mr.Addr())
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return New(client, logger, cfgStore, responses, 120, "8080", "http://localhost:8081", "http://localhost:8100"), mr
}

func TestSessionID_ClientProvided(t *testing.T) {
	body := `{"model":"test","messages":[{"role":"user","content":"hi"}],"session_id":"my-custom-session"}`

	var parsed openAIRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.SessionID != "my-custom-session" {
		t.Errorf("expected session_id 'my-custom-session', got %q", parsed.SessionID)
	}
}

func TestSessionID_GeneratedWhenAbsent(t *testing.T) {
	body := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`

	var parsed openAIRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.SessionID != "" {
		t.Errorf("expected empty session_id, got %q", parsed.SessionID)
	}
}

func TestSessionID_ResponseIncludesSessionID(t *testing.T) {
	resp := openAIResponse{
		ID:        "chatcmpl-test",
		Object:    "chat.completion",
		Created:   1234567890,
		Model:     "test",
		SessionID: "test-session-123",
		Choices: []openAIChoice{{
			Index:   0,
			Message: openAIMsg{Role: "assistant", Content: "hello"},
		}},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	sid, ok := decoded["session_id"].(string)
	if !ok || sid != "test-session-123" {
		t.Errorf("expected session_id 'test-session-123' in response body, got %v", decoded["session_id"])
	}
}

func TestSessionID_OmittedWhenEmpty(t *testing.T) {
	resp := openAIResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "test",
		Choices: []openAIChoice{{
			Index:   0,
			Message: openAIMsg{Role: "assistant", Content: "hello"},
		}},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if _, exists := decoded["session_id"]; exists {
		t.Error("session_id should be omitted when empty")
	}
}

func TestGetConfig_ReturnsAllSections(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	for _, section := range []string{"chat", "memory", "broker", "retro"} {
		if _, ok := result[section]; !ok {
			t.Errorf("missing config section: %s", section)
		}
	}
}

func TestPutConfig_UpdateSection(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"section":"chat","values":{"model":"gpt-4","request_timeout_s":60}}`
	req := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	getW := httptest.NewRecorder()
	srv.ServeHTTP(getW, getReq)

	var result map[string]json.RawMessage
	if err := json.Unmarshal(getW.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	var chat config.ChatConfig
	if err := json.Unmarshal(result["chat"], &chat); err != nil {
		t.Fatal(err)
	}
	if chat.Model != "gpt-4" {
		t.Errorf("expected model 'gpt-4', got %q", chat.Model)
	}
	if chat.RequestTimeoutS != 60 {
		t.Errorf("expected timeout 60, got %d", chat.RequestTimeoutS)
	}
}

func TestPutConfig_InvalidSection(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"section":"invalid","values":{"foo":"bar"}}`
	req := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListSessions(t *testing.T) {
	srv, mr := newTestServer(t)

	seedResponse(t, mr, "resp_001", "abc", "", "user-msg", "assistant-reply")
	seedResponse(t, mr, "resp_002", "def", "", "hi", "hey")

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var sessions []response.SessionSummary
	if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestGetSession_Exists(t *testing.T) {
	srv, mr := newTestServer(t)

	seedResponse(t, mr, "resp_010", "test-sess", "", "hello", "hi there")

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/test-sess", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["session_id"] != "test-sess" {
		t.Errorf("expected session_id 'test-sess', got %v", result["session_id"])
	}
	msgs, ok := result["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %v", result["messages"])
	}
}

func TestGetSession_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeleteSession_Exists(t *testing.T) {
	srv, mr := newTestServer(t)

	seedResponse(t, mr, "resp_del1", "del-sess", "", "bye", "goodbye")

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/del-sess", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if mr.Exists("session:del-sess:responses") {
		t.Error("session response list should be deleted")
	}
	if mr.Exists("response:resp_del1") {
		t.Error("response hash should be deleted")
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestRetroTrigger_ValidReturns202(t *testing.T) {
	srv, mr := newTestServer(t)

	seedResponse(t, mr, "resp_retro1", "retro-sess", "", "hi", "hey")

	body := `{"job_type":"memory_extraction"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/retro/retro-sess/trigger", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "accepted" {
		t.Errorf("expected status 'accepted', got %q", result["status"])
	}
}

func TestRetroTrigger_DuplicateReturns409(t *testing.T) {
	srv, mr := newTestServer(t)

	seedResponse(t, mr, "resp_dup1", "dup-sess", "", "hi", "hey")

	body := `{"job_type":"skill_creation"}`

	req1 := httptest.NewRequest(http.MethodPost, "/v1/retro/dup-sess/trigger", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, req1)

	if w1.Code != http.StatusAccepted {
		t.Fatalf("first trigger: expected 202, got %d", w1.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/retro/dup-sess/trigger", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Fatalf("second trigger: expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestRetroTrigger_InvalidJobTypeReturns400(t *testing.T) {
	srv, mr := newTestServer(t)

	seedResponse(t, mr, "resp_bad1", "bad-job", "", "hi", "hey")

	body := `{"job_type":"nonexistent_job"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/retro/bad-job/trigger", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRetroTrigger_MissingSessionReturns404(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"job_type":"memory_extraction"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/retro/nonexistent/trigger", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRetroTrigger_LockReleasedAfterCompletion(t *testing.T) {
	srv, mr := newTestServer(t)

	seedResponse(t, mr, "resp_lock1", "lock-test", "", "hi", "hey")

	body := `{"job_type":"curation"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/retro/lock-test/trigger", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}

	if !mr.Exists("retro:lock:lock-test:curation") {
		t.Error("lock should exist after trigger")
	}

	mr.Del("retro:lock:lock-test:curation")

	req2 := httptest.NewRequest(http.MethodPost, "/v1/retro/lock-test/trigger", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	if w2.Code != http.StatusAccepted {
		t.Fatalf("after lock release: expected 202, got %d", w2.Code)
	}
}

func TestStatus_AllHealthy(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	if len(result.Services) != 3 {
		t.Fatalf("expected 3 services, got %d", len(result.Services))
	}

	valkeyHealth := result.Services[0]
	if valkeyHealth.Name != "valkey" || valkeyHealth.Status != "healthy" {
		t.Errorf("expected valkey healthy, got %+v", valkeyHealth)
	}

	if result.System.GatewayPort != "8080" {
		t.Errorf("expected gateway port '8080', got %q", result.System.GatewayPort)
	}
}

func TestStatus_PartialFailure(t *testing.T) {
	srv, _ := newTestServer(t)

	badHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer badHTTP.Close()

	srv.llamaAddr = badHTTP.URL
	srv.muninnAddr = badHTTP.URL

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	valkeyHealth := result.Services[0]
	if valkeyHealth.Status != "healthy" {
		t.Errorf("expected valkey healthy, got %s", valkeyHealth.Status)
	}

	for _, svc := range result.Services[1:] {
		if svc.Status != "unhealthy" {
			t.Errorf("expected %s unhealthy, got %s", svc.Name, svc.Status)
		}
	}
}

func TestStatus_AgentRegistryIncluded(t *testing.T) {
	srv, mr := newTestServer(t)

	agentData := `{"agent_id":"test-agent","priority":5,"preemptible":true,"capabilities":["chat","memory_extraction"],"trigger":"event-driven","heartbeat_interval_ms":3000}`
	mr.Set("agent:test-agent", agentData)

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	if len(result.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(result.Agents))
	}

	agent := result.Agents[0]
	if agent.AgentID != "test-agent" {
		t.Errorf("expected agent_id 'test-agent', got %q", agent.AgentID)
	}
	if agent.Priority != 5 {
		t.Errorf("expected priority 5, got %d", agent.Priority)
	}
	if !agent.Preemptible {
		t.Error("expected preemptible true")
	}
	if len(agent.Capabilities) != 2 {
		t.Errorf("expected 2 capabilities, got %d", len(agent.Capabilities))
	}
}

func TestStatus_NoAgents(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var result statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	if result.Agents == nil {
		t.Error("agents should be empty array, not null")
	}
	if len(result.Agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(result.Agents))
	}
}

func TestDashboard_ServesIndexHTML(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "microagent2") {
		t.Error("expected dashboard HTML to contain 'microagent2'")
	}
	if !strings.Contains(body, "app.js") {
		t.Error("expected dashboard HTML to reference app.js")
	}
}

func TestDashboard_ServesCSS(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestDashboard_DoesNotConflictWithAPI(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from API, got %d", w.Code)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal("API response should be JSON, not static file")
	}
}

func TestStatus_MultipleAgents(t *testing.T) {
	srv, mr := newTestServer(t)

	for i := 0; i < 3; i++ {
		agentData := fmt.Sprintf(`{"agent_id":"agent-%d","priority":%d,"preemptible":true,"capabilities":["chat"],"trigger":"event-driven","heartbeat_interval_ms":3000}`, i, i)
		mr.Set(fmt.Sprintf("agent:agent-%d", i), agentData)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var result statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	if len(result.Agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(result.Agents))
	}
}

// --- Responses API Tests ---

func TestGetResponse_Found(t *testing.T) {
	srv, mr := newTestServer(t)

	seedResponse(t, mr, "resp_GET1", "sess-get", "", "hello", "world")

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_GET1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result responsesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "resp_GET1" {
		t.Errorf("expected id resp_GET1, got %q", result.ID)
	}
	if result.Object != "response" {
		t.Errorf("expected object 'response', got %q", result.Object)
	}
	if result.SessionID != "sess-get" {
		t.Errorf("expected session sess-get, got %q", result.SessionID)
	}
	if result.Status != response.StatusCompleted {
		t.Errorf("expected completed, got %q", result.Status)
	}
	if len(result.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(result.Output))
	}
	if result.Output[0].Content[0].Text != "world" {
		t.Errorf("expected output text 'world', got %q", result.Output[0].Content[0].Text)
	}
}

func TestGetResponse_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_MISSING", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestCreateResponse_InvalidInput(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"input":123,"model":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateResponse_EmptyInput(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"input":[],"model":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateResponse_BrokenChain(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"input":"hello","model":"test","previous_response_id":"resp_DOESNOTEXIST"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestParseInput_String(t *testing.T) {
	items, err := parseInput("hello world")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Role != "user" {
		t.Errorf("expected role user, got %q", items[0].Role)
	}
	if items[0].Content != "hello world" {
		t.Errorf("expected content 'hello world', got %v", items[0].Content)
	}
}

func TestParseInput_Array(t *testing.T) {
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "hi"},
		map[string]any{"type": "message", "role": "assistant", "content": "hey"},
	}
	items, err := parseInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestChainToMessages(t *testing.T) {
	chain := []*response.Response{
		{
			Input:  []response.InputItem{{Type: "message", Role: "user", Content: "first"}},
			Output: []response.OutputItem{{Type: "message", Role: "assistant", Content: []response.ContentPart{{Type: "output_text", Text: "reply1"}}}},
		},
		{
			Input:  []response.InputItem{{Type: "message", Role: "user", Content: "second"}},
			Output: []response.OutputItem{{Type: "message", Role: "assistant", Content: []response.ContentPart{{Type: "output_text", Text: "reply2"}}}},
		},
	}

	msgs := chainToMessages(chain)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "first" || msgs[1].Content != "reply1" || msgs[2].Content != "second" || msgs[3].Content != "reply2" {
		t.Errorf("message content mismatch: %v", msgs)
	}
}

func TestTextToOutputItems(t *testing.T) {
	items := textToOutputItems("hello world")
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Type != "message" || items[0].Role != "assistant" {
		t.Errorf("unexpected item type/role: %+v", items[0])
	}
	if len(items[0].Content) != 1 || items[0].Content[0].Text != "hello world" {
		t.Errorf("expected text 'hello world', got %+v", items[0].Content)
	}
}

func TestSessionReconstructionFromResponseChain(t *testing.T) {
	srv, mr := newTestServer(t)

	seedResponse(t, mr, "resp_chain1", "sess-recon", "", "first msg", "first reply")
	seedResponse(t, mr, "resp_chain2", "sess-recon", "resp_chain1", "second msg", "second reply")

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/sess-recon", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	msgs, ok := result["messages"].([]any)
	if !ok {
		t.Fatal("expected messages array")
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages (2 user + 2 assistant), got %d", len(msgs))
	}
}

func TestDeleteSession_RemovesResponseHashes(t *testing.T) {
	srv, mr := newTestServer(t)

	seedResponse(t, mr, "resp_dh1", "sess-dh", "", "hi", "hey")
	seedResponse(t, mr, "resp_dh2", "sess-dh", "resp_dh1", "bye", "cya")

	if !mr.Exists("response:resp_dh1") || !mr.Exists("response:resp_dh2") {
		t.Fatal("responses should exist before delete")
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/sess-dh", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	if mr.Exists("response:resp_dh1") {
		t.Error("resp_dh1 should be deleted")
	}
	if mr.Exists("response:resp_dh2") {
		t.Error("resp_dh2 should be deleted")
	}
	if mr.Exists("session:sess-dh:responses") {
		t.Error("session response list should be deleted")
	}
}

func seedResponse(t *testing.T, mr *miniredis.Miniredis, respID, sessionID, prevRespID, userContent, assistantContent string) {
	t.Helper()
	inputJSON, _ := json.Marshal([]response.InputItem{{Type: "message", Role: "user", Content: userContent}})
	outputJSON, _ := json.Marshal([]response.OutputItem{{
		Type: "message",
		Role: "assistant",
		Content: []response.ContentPart{{Type: "output_text", Text: assistantContent}},
	}})
	key := "response:" + respID
	mr.HSet(key, "id", respID)
	mr.HSet(key, "input", string(inputJSON))
	mr.HSet(key, "output", string(outputJSON))
	mr.HSet(key, "previous_response_id", prevRespID)
	mr.HSet(key, "session_id", sessionID)
	mr.HSet(key, "model", "test")
	mr.HSet(key, "created_at", "2025-01-01T00:00:00Z")
	mr.HSet(key, "status", "completed")
	mr.RPush("session:"+sessionID+":responses", respID)
}
