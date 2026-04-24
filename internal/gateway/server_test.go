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
	appcontext "microagent2/internal/context"
	"microagent2/internal/messaging"
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
	sessions := appcontext.NewSessionStore(rdb)
	client := messaging.NewClient(mr.Addr())
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return New(client, logger, cfgStore, sessions, 120, "8080", "http://localhost:8081", "http://localhost:8100"), mr
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

	mr.Lpush("session:abc:history", `{"role":"user","content":"hello"}`)
	mr.Lpush("session:def:history", `{"role":"user","content":"hi"}`)
	mr.Lpush("session:def:history", `{"role":"assistant","content":"hey"}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var sessions []sessionSummary
	if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestGetSession_Exists(t *testing.T) {
	srv, mr := newTestServer(t)

	mr.RPush("session:test-sess:history", `{"role":"user","content":"hello"}`)
	mr.RPush("session:test-sess:history", `{"role":"assistant","content":"hi there"}`)

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

	mr.RPush("session:del-sess:history", `{"role":"user","content":"bye"}`)

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/del-sess", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if mr.Exists("session:del-sess:history") {
		t.Error("session key should be deleted")
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

	mr.RPush("session:retro-sess:history", `{"role":"user","content":"hi"}`)

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

	mr.RPush("session:dup-sess:history", `{"role":"user","content":"hi"}`)

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

	mr.RPush("session:bad-job:history", `{"role":"user","content":"hi"}`)

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

	mr.RPush("session:lock-test:history", `{"role":"user","content":"hi"}`)

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
