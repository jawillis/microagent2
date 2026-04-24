package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/redis/go-redis/v9"

	"microagent2/internal/config"
)

const mcpHealthKey = "health:main-agent:mcp"

func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	servers := config.ResolveMCPServers(r.Context(), s.configStore, s.logger)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"servers": servers})
}

func (s *Server) handlePutMCPServers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Servers []config.MCPServerConfig `json:"servers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "failed to decode body")
		return
	}
	if body.Servers == nil {
		body.Servers = []config.MCPServerConfig{}
	}
	if err := config.SaveMCPServers(r.Context(), s.configStore, body.Servers); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAddMCPServer(w http.ResponseWriter, r *http.Request) {
	var entry config.MCPServerConfig
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "failed to decode body")
		return
	}
	existing := config.ResolveMCPServers(r.Context(), s.configStore, s.logger)
	for _, e := range existing {
		if e.Name == entry.Name {
			writeError(w, http.StatusConflict, "conflict", "server with this name already exists")
			return
		}
	}
	updated := append(existing, entry)
	if err := config.SaveMCPServers(r.Context(), s.configStore, updated); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(entry)
}

func (s *Server) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	existing := config.ResolveMCPServers(r.Context(), s.configStore, s.logger)
	updated := make([]config.MCPServerConfig, 0, len(existing))
	found := false
	for _, e := range existing {
		if e.Name == name {
			found = true
			continue
		}
		updated = append(updated, e)
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "server not found")
		return
	}
	if err := config.SaveMCPServers(r.Context(), s.configStore, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readMCPHealth reads the health snapshot written by main-agent and returns
// the parsed array, or an empty slice if absent.
func readMCPHealth(r *http.Request, rdb *redis.Client) []json.RawMessage {
	raw, err := rdb.Get(r.Context(), mcpHealthKey).Result()
	if err != nil || raw == "" {
		return []json.RawMessage{}
	}
	var out []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []json.RawMessage{}
	}
	return out
}
