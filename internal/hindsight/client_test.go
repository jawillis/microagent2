package hindsight

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testServer returns an httptest.Server plus a *Client pointing at it.
func testServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, "test-key")
}

func TestRetainSuccess(t *testing.T) {
	srv, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/default/banks/microagent2/memories" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type: %q", r.Header.Get("Content-Type"))
		}
		var body RetainRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Items) != 1 || body.Items[0].Content != "hello" {
			t.Errorf("body: %+v", body)
		}
		if got := body.Items[0].Metadata["provenance"]; got != "explicit" {
			t.Errorf("metadata.provenance = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"bank_id":"microagent2","items_count":1,"async":false}`)
	})
	_ = srv

	resp, err := c.Retain(context.Background(), "microagent2", RetainRequest{
		Items: []MemoryItem{{
			Content:  "hello",
			Metadata: map[string]string{"provenance": "explicit"},
		}},
	})
	if err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if !resp.Success || resp.ItemsCount != 1 {
		t.Fatalf("response: %+v", resp)
	}
}

func TestRecallHonorsTypes(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body RecallRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Types) != 1 || body.Types[0] != "observation" {
			t.Errorf("types = %v", body.Types)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"id":"m1","text":"fact","type":"observation"}],"source_facts":{"f1":{"id":"f1","text":"raw"}}}`)
	})

	resp, err := c.Recall(context.Background(), "microagent2", RecallRequest{
		Query: "anything",
		Types: []string{"observation"},
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != "m1" {
		t.Fatalf("results: %+v", resp.Results)
	}
	if _, ok := resp.SourceFacts["f1"]; !ok {
		t.Fatalf("source_facts missing f1: %+v", resp.SourceFacts)
	}
}

func TestNon2xxReturnsTypedError(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"detail":"invalid"}`)
	})

	_, err := c.Retain(context.Background(), "microagent2", RetainRequest{
		Items: []MemoryItem{{Content: "x"}},
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var he *Error
	if !errors.As(err, &he) {
		t.Fatalf("want *Error, got %T: %v", err, err)
	}
	if he.StatusCode != 400 {
		t.Fatalf("status = %d", he.StatusCode)
	}
	if !strings.Contains(he.Body, "invalid") {
		t.Fatalf("body = %q", he.Body)
	}
}

func TestContextCancellation(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Recall(ctx, "microagent2", RecallRequest{Query: "x"})
	if err == nil {
		t.Fatal("want timeout error")
	}
}

func TestGetBankConfig(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		_, _ = io.WriteString(w, `{"bank_id":"microagent2","config":{"retain_mission":"m","llm_model":"gpt"},"overrides":{"retain_mission":"m"}}`)
	})

	cfg, err := c.GetBankConfig(context.Background(), "microagent2")
	if err != nil {
		t.Fatalf("GetBankConfig: %v", err)
	}
	if cfg.BankID != "microagent2" {
		t.Fatalf("bank_id = %q", cfg.BankID)
	}
	if cfg.Config["retain_mission"] != "m" {
		t.Fatalf("retain_mission not extracted: %+v", cfg.Config)
	}
	if cfg.Overrides["retain_mission"] != "m" {
		t.Fatalf("overrides not extracted: %+v", cfg.Overrides)
	}
}

func TestPatchBankConfigSendsUpdates(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		var body BankConfigUpdate
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Updates["retain_mission"] != "new" {
			t.Errorf("updates: %+v", body.Updates)
		}
		_, _ = io.WriteString(w, `{"bank_id":"microagent2","config":{},"overrides":{"retain_mission":"new"}}`)
	})

	resp, err := c.PatchBankConfig(context.Background(), "microagent2", BankConfigUpdate{
		Updates: map[string]interface{}{"retain_mission": "new"},
	})
	if err != nil {
		t.Fatalf("PatchBankConfig: %v", err)
	}
	if resp.Overrides["retain_mission"] != "new" {
		t.Fatalf("not reflected in response: %+v", resp.Overrides)
	}
}

func TestListDirectives(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"items":[{"id":"d1","bank_id":"b","name":"n","content":"c","priority":5,"is_active":true,"tags":["t1"]}]}`)
	})

	resp, err := c.ListDirectives(context.Background(), "microagent2")
	if err != nil {
		t.Fatalf("ListDirectives: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].ID != "d1" || resp.Items[0].Priority != 5 {
		t.Fatalf("items: %+v", resp.Items)
	}
}

func TestCreateDirective(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		var body CreateDirectiveRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Name != "n" || body.Content != "c" || body.Priority != 90 || !body.IsActive {
			t.Errorf("body: %+v", body)
		}
		_, _ = io.WriteString(w, `{"id":"d1","bank_id":"b","name":"n","content":"c","priority":90,"is_active":true,"tags":[]}`)
	})

	d, err := c.CreateDirective(context.Background(), "microagent2", CreateDirectiveRequest{
		Name: "n", Content: "c", Priority: 90, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateDirective: %v", err)
	}
	if d.ID != "d1" {
		t.Fatalf("id = %q", d.ID)
	}
}

func TestCreateWebhook(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body CreateWebhookRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.URL != "http://memory-service/hook" {
			t.Errorf("url: %q", body.URL)
		}
		if body.Secret != "shh" {
			t.Errorf("secret: %q", body.Secret)
		}
		if !body.Enabled {
			t.Errorf("enabled should be true")
		}
		_, _ = io.WriteString(w, `{"id":"w1","url":"http://memory-service/hook","event_types":["retain.completed"],"enabled":true}`)
	})

	wh, err := c.CreateWebhook(context.Background(), "microagent2", CreateWebhookRequest{
		URL:        "http://memory-service/hook",
		Secret:     "shh",
		EventTypes: []string{"retain.completed"},
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	if wh.ID != "w1" {
		t.Fatalf("id = %q", wh.ID)
	}
}

func TestDeleteMemory(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/default/banks/microagent2/memories/m1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.DeleteMemory(context.Background(), "microagent2", "m1"); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
}

func TestDeleteMemoryNotFound(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"detail":"not found"}`)
	})

	err := c.DeleteMemory(context.Background(), "microagent2", "missing")
	if err == nil {
		t.Fatal("want error")
	}
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound = false, err = %v", err)
	}
}

func TestReflectSuccess(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body ReflectRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Query != "what do I like?" {
			t.Errorf("query: %q", body.Query)
		}
		_, _ = io.WriteString(w, `{"text":"markdown response"}`)
	})
	out, err := c.Reflect(context.Background(), "microagent2", ReflectRequest{Query: "what do I like?"})
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if out.Text != "markdown response" {
		t.Fatalf("text: %q", out.Text)
	}
}

func TestConsolidate(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/default/banks/microagent2/consolidate" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"operation_id":"op-1"}`)
	})

	out, err := c.Consolidate(context.Background(), "microagent2")
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if out["operation_id"] != "op-1" {
		t.Fatalf("response: %+v", out)
	}
}

func TestListBanks(t *testing.T) {
	_, c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"banks":[{"bank_id":"b1","disposition":{"skepticism":3,"literalism":3,"empathy":3}}]}`)
	})
	resp, err := c.ListBanks(context.Background())
	if err != nil {
		t.Fatalf("ListBanks: %v", err)
	}
	if len(resp.Banks) != 1 || resp.Banks[0].BankID != "b1" {
		t.Fatalf("banks: %+v", resp.Banks)
	}
}
