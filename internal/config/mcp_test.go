package config

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestResolveMCPServers_Empty(t *testing.T) {
	store, _ := testStore(t)
	got := ResolveMCPServers(context.Background(), store, silentLogger())
	if got == nil || len(got) != 0 {
		t.Fatalf("want empty slice, got %+v", got)
	}
}

func TestResolveMCPServers_RoundTrip(t *testing.T) {
	store, _ := testStore(t)
	want := []MCPServerConfig{
		{Name: "a", Enabled: true, Command: "echo", Args: []string{"hi"}},
		{Name: "b_2", Enabled: false, Command: "sleep", Args: []string{"1"}, Env: map[string]string{"K": "V"}},
	}
	if err := SaveMCPServers(context.Background(), store, want); err != nil {
		t.Fatal(err)
	}
	got := ResolveMCPServers(context.Background(), store, silentLogger())
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b_2" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveMCPServers_InvalidEntriesSkipped(t *testing.T) {
	store, mr := testStore(t)
	raw, _ := json.Marshal([]map[string]any{
		{"name": "", "command": "x"},                   // missing name
		{"name": "bad name", "command": "x"},           // invalid chars
		{"name": "ok", "command": "echo"},              // valid
		{"name": "nocmd"},                              // missing command
		{"name": "ok", "command": "echo"},              // duplicate
	})
	_ = mr.Set(KeyMCPServers, string(raw))
	got := ResolveMCPServers(context.Background(), store, silentLogger())
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("got %+v", got)
	}
}

func TestSaveMCPServers_RejectsInvalidName(t *testing.T) {
	store, _ := testStore(t)
	err := SaveMCPServers(context.Background(), store, []MCPServerConfig{
		{Name: "bad name", Command: "x"},
	})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestSaveMCPServers_RejectsDuplicates(t *testing.T) {
	store, _ := testStore(t)
	err := SaveMCPServers(context.Background(), store, []MCPServerConfig{
		{Name: "a", Command: "x"},
		{Name: "a", Command: "y"},
	})
	if err == nil {
		t.Fatal("want duplicate error")
	}
}
