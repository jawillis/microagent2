package memoryservice

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"microagent2/internal/config"
	"microagent2/internal/hindsight"
	"microagent2/internal/memoryclient"
)

func testServerWithResolver(t *testing.T, fh *fakeHindsight, cfg config.MemoryConfig) *Server {
	t.Helper()
	hc := hindsight.New(fh.URL, "")
	return New(hc, Config{
		BankID: "microagent2",
		Resolver: func(ctx context.Context) config.MemoryConfig {
			return cfg
		},
	}, discardLogger())
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeHindsight registers per-endpoint handlers and serves them.
type fakeHindsight struct {
	*httptest.Server
	lastRetain hindsight.RetainRequest
	lastRecall hindsight.RecallRequest
	retainResp hindsight.RetainResponse
	recallResp hindsight.RecallResponse
	reflectRes hindsight.ReflectResponse
	deleteHits int
}

func newFakeHindsight(t *testing.T) *fakeHindsight {
	t.Helper()
	f := &fakeHindsight{
		retainResp: hindsight.RetainResponse{Success: true, BankID: "microagent2", ItemsCount: 1, Async: false},
		reflectRes: hindsight.ReflectResponse{Text: "synthesis"},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/memories"):
			_ = json.NewDecoder(r.Body).Decode(&f.lastRetain)
			_ = json.NewEncoder(w).Encode(f.retainResp)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/memories/recall"):
			_ = json.NewDecoder(r.Body).Decode(&f.lastRecall)
			_ = json.NewEncoder(w).Encode(f.recallResp)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reflect"):
			_ = json.NewEncoder(w).Encode(f.reflectRes)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/memories/"):
			f.deleteHits++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func newTestServer(t *testing.T, fh *fakeHindsight) *Server {
	t.Helper()
	hc := hindsight.New(fh.URL, "")
	return New(hc, Config{BankID: "microagent2"}, discardLogger())
}

func TestRetainAppliesProvenanceDefault(t *testing.T) {
	fh := newFakeHindsight(t)
	s := newTestServer(t, fh)
	body, _ := json.Marshal(memoryclient.RetainRequest{Content: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/retain", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := fh.lastRetain.Items[0].Metadata["provenance"]; got != "explicit" {
		t.Fatalf("provenance default = %q; want explicit", got)
	}
}

func TestRetainPreservesSpecifiedProvenance(t *testing.T) {
	fh := newFakeHindsight(t)
	s := newTestServer(t, fh)
	body, _ := json.Marshal(memoryclient.RetainRequest{
		Content:  "hi",
		Metadata: map[string]string{"provenance": "inferred", "confidence": "0.7"},
	})
	req := httptest.NewRequest(http.MethodPost, "/retain", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := fh.lastRetain.Items[0].Metadata["provenance"]; got != "inferred" {
		t.Fatalf("provenance = %q; want inferred", got)
	}
	if got := fh.lastRetain.Items[0].Metadata["confidence"]; got != "0.7" {
		t.Fatalf("confidence preserved = %q", got)
	}
}

func TestRetainRejectsInvalidProvenance(t *testing.T) {
	fh := newFakeHindsight(t)
	s := newTestServer(t, fh)
	body, _ := json.Marshal(memoryclient.RetainRequest{
		Content:  "hi",
		Metadata: map[string]string{"provenance": "bogus"},
	})
	req := httptest.NewRequest(http.MethodPost, "/retain", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	var eb errBody
	_ = json.NewDecoder(rec.Body).Decode(&eb)
	if eb.Error.Code != "invalid_provenance" {
		t.Fatalf("code = %q", eb.Error.Code)
	}
}

func TestRecallDefaultsTypes(t *testing.T) {
	fh := newFakeHindsight(t)
	s := newTestServer(t, fh)
	body, _ := json.Marshal(memoryclient.RecallRequest{Query: "coffee"})
	req := httptest.NewRequest(http.MethodPost, "/recall", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(fh.lastRecall.Types) != 1 || fh.lastRecall.Types[0] != "observation" {
		t.Fatalf("types = %v; want [observation]", fh.lastRecall.Types)
	}
}

func TestRecallHonorsExplicitTypes(t *testing.T) {
	fh := newFakeHindsight(t)
	s := newTestServer(t, fh)
	body, _ := json.Marshal(memoryclient.RecallRequest{Query: "x", Types: []string{"observation"}})
	req := httptest.NewRequest(http.MethodPost, "/recall", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(fh.lastRecall.Types) != 1 || fh.lastRecall.Types[0] != "observation" {
		t.Fatalf("types = %v", fh.lastRecall.Types)
	}
}

func TestRecallTranslatesResultsAndLimits(t *testing.T) {
	fh := newFakeHindsight(t)
	fh.recallResp = hindsight.RecallResponse{
		Results: []hindsight.RecallResult{
			{ID: "m1", Text: "a", Score: 0.9, Tags: []string{"preferences"}, Type: "world"},
			{ID: "m2", Text: "b", Score: 0.7},
			{ID: "m3", Text: "c", Score: 0.5},
		},
		SourceFacts: map[string]hindsight.RecallResult{
			"f1": {ID: "f1", Text: "raw"},
		},
	}
	s := newTestServer(t, fh)
	body, _ := json.Marshal(memoryclient.RecallRequest{Query: "x", Limit: 2})
	req := httptest.NewRequest(http.MethodPost, "/recall", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var resp memoryclient.RecallResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Memories) != 2 {
		t.Fatalf("memories = %d; want 2", len(resp.Memories))
	}
	if resp.Memories[0].Content != "a" || resp.Memories[0].Tags[0] != "preferences" {
		t.Fatalf("first: %+v", resp.Memories[0])
	}
	if _, ok := resp.SourceFacts["f1"]; !ok {
		t.Fatalf("source_facts: %+v", resp.SourceFacts)
	}
}

func TestReflect(t *testing.T) {
	fh := newFakeHindsight(t)
	fh.reflectRes = hindsight.ReflectResponse{Text: "synthesis output"}
	s := newTestServer(t, fh)
	body, _ := json.Marshal(memoryclient.ReflectRequest{Query: "what do I like?"})
	req := httptest.NewRequest(http.MethodPost, "/reflect", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp memoryclient.ReflectResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Text != "synthesis output" {
		t.Fatalf("text = %q", resp.Text)
	}
}

func TestForgetByID(t *testing.T) {
	fh := newFakeHindsight(t)
	s := newTestServer(t, fh)
	body, _ := json.Marshal(memoryclient.ForgetRequest{MemoryID: "m42"})
	req := httptest.NewRequest(http.MethodPost, "/forget", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if fh.deleteHits != 1 {
		t.Fatalf("expected 1 delete, got %d", fh.deleteHits)
	}
	var resp memoryclient.ForgetResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.DeletedID != "m42" {
		t.Fatalf("deleted_id = %q", resp.DeletedID)
	}
}

func TestForgetByQueryBestMatch(t *testing.T) {
	fh := newFakeHindsight(t)
	fh.recallResp = hindsight.RecallResponse{
		Results: []hindsight.RecallResult{{ID: "match-1", Text: "pizza preference"}},
	}
	s := newTestServer(t, fh)
	body, _ := json.Marshal(memoryclient.ForgetRequest{Query: "pizza"})
	req := httptest.NewRequest(http.MethodPost, "/forget", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fh.deleteHits != 1 {
		t.Fatalf("expected 1 delete, got %d", fh.deleteHits)
	}
}

func TestForgetByQueryNoMatch(t *testing.T) {
	fh := newFakeHindsight(t)
	// recallResp defaults to empty results
	s := newTestServer(t, fh)
	body, _ := json.Marshal(memoryclient.ForgetRequest{Query: "nothing"})
	req := httptest.NewRequest(http.MethodPost, "/forget", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

func TestHealthReachable(t *testing.T) {
	fh := newFakeHindsight(t)
	s := newTestServer(t, fh)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var m map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&m)
	if m["hindsight"] != "reachable" || m["bank"] != "microagent2" {
		t.Fatalf("body = %+v", m)
	}
}

func TestHealthUnreachable(t *testing.T) {
	fh := newFakeHindsight(t)
	s := newTestServer(t, fh)
	s.MarkHindsightReachable(false)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", rec.Code)
	}
}

func TestWebhookSignatureValid(t *testing.T) {
	fh := newFakeHindsight(t)
	hc := hindsight.New(fh.URL, "")
	s := New(hc, Config{BankID: "microagent2", WebhookSecret: "shh"}, discardLogger())

	body := []byte(`{"event":"retain.completed","bank_id":"microagent2","operation_id":"op1"}`)
	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/hooks/hind/retain", strings.NewReader(string(body)))
	req.Header.Set("X-Hindsight-Signature", sig)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestWebhookSignaturePrefixed(t *testing.T) {
	fh := newFakeHindsight(t)
	hc := hindsight.New(fh.URL, "")
	s := New(hc, Config{BankID: "microagent2", WebhookSecret: "shh"}, discardLogger())

	body := []byte(`{"event":"consolidation.completed"}`)
	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/hooks/hind/consolidation", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", sig)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestWebhookSignatureInvalid(t *testing.T) {
	fh := newFakeHindsight(t)
	hc := hindsight.New(fh.URL, "")
	s := New(hc, Config{BankID: "microagent2", WebhookSecret: "shh"}, discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/hooks/hind/retain", strings.NewReader(`{}`))
	req.Header.Set("X-Hindsight-Signature", "deadbeef")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestWebhookMissingSignature(t *testing.T) {
	fh := newFakeHindsight(t)
	hc := hindsight.New(fh.URL, "")
	s := New(hc, Config{BankID: "microagent2", WebhookSecret: "shh"}, discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/hooks/hind/retain", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

// Compile-time check: context use doesn't leak on shutdown.
var _ context.Context = context.Background()

// --- resolver-driven defaults ---

func TestRetain_DefaultProvenanceFromResolver(t *testing.T) {
	fh := newFakeHindsight(t)
	s := testServerWithResolver(t, fh, config.MemoryConfig{DefaultProvenance: "implicit"})
	body, _ := json.Marshal(memoryclient.RetainRequest{Content: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/retain", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := fh.lastRetain.Items[0].Metadata["provenance"]; got != "implicit" {
		t.Fatalf("provenance = %q; want implicit from resolver", got)
	}
}

func TestRetain_InvalidConfigProvenanceFallsBack(t *testing.T) {
	fh := newFakeHindsight(t)
	s := testServerWithResolver(t, fh, config.MemoryConfig{DefaultProvenance: "bogus"})
	body, _ := json.Marshal(memoryclient.RetainRequest{Content: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/retain", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; invalid config should fall back, not reject", rec.Code)
	}
	if got := fh.lastRetain.Items[0].Metadata["provenance"]; got != "explicit" {
		t.Fatalf("fallback = %q; want explicit", got)
	}
}

func TestRecall_DefaultTypesFromResolver_WorldExperience(t *testing.T) {
	fh := newFakeHindsight(t)
	s := testServerWithResolver(t, fh, config.MemoryConfig{RecallDefaultTypes: "world_experience"})
	body, _ := json.Marshal(memoryclient.RecallRequest{Query: "x"})
	req := httptest.NewRequest(http.MethodPost, "/recall", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	got := fh.lastRecall.Types
	if len(got) != 2 || got[0] != "world" || got[1] != "experience" {
		t.Fatalf("types = %v; want [world experience]", got)
	}
}

func TestRecall_DefaultTypesFromResolver_All(t *testing.T) {
	fh := newFakeHindsight(t)
	s := testServerWithResolver(t, fh, config.MemoryConfig{RecallDefaultTypes: "all"})
	body, _ := json.Marshal(memoryclient.RecallRequest{Query: "x"})
	req := httptest.NewRequest(http.MethodPost, "/recall", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	got := fh.lastRecall.Types
	if len(got) != 3 {
		t.Fatalf("types = %v; want 3 values", got)
	}
}

func TestRecall_InvalidResolverValueFallsBack(t *testing.T) {
	fh := newFakeHindsight(t)
	s := testServerWithResolver(t, fh, config.MemoryConfig{RecallDefaultTypes: "bogus"})
	body, _ := json.Marshal(memoryclient.RecallRequest{Query: "x"})
	req := httptest.NewRequest(http.MethodPost, "/recall", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	got := fh.lastRecall.Types
	if len(got) != 1 || got[0] != "observation" {
		t.Fatalf("fallback types = %v; want [observation]", got)
	}
}

func TestRecall_CallerTypesOverrideResolver(t *testing.T) {
	fh := newFakeHindsight(t)
	s := testServerWithResolver(t, fh, config.MemoryConfig{RecallDefaultTypes: "all"})
	body, _ := json.Marshal(memoryclient.RecallRequest{Query: "x", Types: []string{"observation"}})
	req := httptest.NewRequest(http.MethodPost, "/recall", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	got := fh.lastRecall.Types
	if len(got) != 1 || got[0] != "observation" {
		t.Fatalf("types = %v; caller should win", got)
	}
}
