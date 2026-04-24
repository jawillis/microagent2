package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"microagent2/internal/config"
)

func TestMCPServers_GetEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/mcp/servers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}
	var body struct {
		Servers []config.MCPServerConfig `json:"servers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Servers) != 0 {
		t.Fatalf("want empty, got %+v", body.Servers)
	}
}

func TestMCPServers_PutReplaceList(t *testing.T) {
	srv, _ := newTestServer(t)
	body := `{"servers":[{"name":"a","enabled":true,"command":"echo"},{"name":"b","enabled":false,"command":"echo"}]}`
	req := httptest.NewRequest(http.MethodPut, "/v1/mcp/servers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	got := config.ResolveMCPServers(context.Background(), srv.configStore, srv.logger)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestMCPServers_PutDuplicateRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	body := `{"servers":[{"name":"a","command":"echo"},{"name":"a","command":"echo"}]}`
	req := httptest.NewRequest(http.MethodPut, "/v1/mcp/servers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestMCPServers_PostAdd(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp/servers", strings.NewReader(`{"name":"new","command":"echo"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestMCPServers_PostDuplicateConflict(t *testing.T) {
	srv, _ := newTestServer(t)
	_ = config.SaveMCPServers(context.Background(), srv.configStore, []config.MCPServerConfig{{Name: "a", Command: "echo"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp/servers", strings.NewReader(`{"name":"a","command":"echo"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestMCPServers_DeleteExisting(t *testing.T) {
	srv, _ := newTestServer(t)
	_ = config.SaveMCPServers(context.Background(), srv.configStore, []config.MCPServerConfig{{Name: "a", Command: "echo"}})
	req := httptest.NewRequest(http.MethodDelete, "/v1/mcp/servers/a", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status: %d", w.Code)
	}
	got := config.ResolveMCPServers(context.Background(), srv.configStore, srv.logger)
	if len(got) != 0 {
		t.Fatalf("not deleted: %+v", got)
	}
}

func TestMCPServers_DeleteMissing(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/v1/mcp/servers/ghost", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestStatus_MCPServersFieldPresent(t *testing.T) {
	srv, mr := newTestServer(t)
	mr.Set("health:main-agent:mcp", `[{"name":"alpha","enabled":true,"connected":true,"tool_count":3}]`)
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}
	var out statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.MCPServers) != 1 {
		t.Fatalf("mcp_servers: %+v", out.MCPServers)
	}
}

func TestStatus_MCPServersEmptyWhenAbsent(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var out statusResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.MCPServers == nil {
		t.Fatal("mcp_servers must not be nil")
	}
	if len(out.MCPServers) != 0 {
		t.Fatalf("expected empty, got %d", len(out.MCPServers))
	}
}
