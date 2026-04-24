package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"microagent2/internal/messaging"
	"microagent2/internal/skills"
)

type listSkillsTool struct {
	store *skills.Store
}

func NewListSkills(store *skills.Store) Tool { return &listSkillsTool{store: store} }

func (t *listSkillsTool) Name() string { return "list_skills" }

func (t *listSkillsTool) Schema() messaging.ToolSchema {
	params := json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
	return messaging.ToolSchema{
		Type: "function",
		Function: messaging.ToolFunction{
			Name:        "list_skills",
			Description: "List all available skills. Returns a JSON array of objects with name and description fields. Call this to discover what skills can be loaded with read_skill.",
			Parameters:  params,
		},
	}
}

func (t *listSkillsTool) Invoke(ctx context.Context, argsJSON string) (string, error) {
	entries := make([]ManifestEntry, 0)
	for _, m := range t.store.List() {
		entries = append(entries, ManifestEntry{Name: m.Name, Description: m.Description})
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type readSkillTool struct {
	store *skills.Store
}

func NewReadSkill(store *skills.Store) Tool { return &readSkillTool{store: store} }

func (t *readSkillTool) Name() string { return "read_skill" }

func (t *readSkillTool) Schema() messaging.ToolSchema {
	params := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Exact skill name as returned by list_skills"}},"required":["name"]}`)
	return messaging.ToolSchema{
		Type: "function",
		Function: messaging.ToolFunction{
			Name:        "read_skill",
			Description: "Load the full instructions for a named skill and apply them. Use this when the user's task matches a skill's description. The returned content is authoritative skill instructions.",
			Parameters:  params,
		},
	}
}

func (t *readSkillTool) Invoke(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError(fmt.Sprintf("invalid arguments: %s", err.Error())), nil
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return jsonError("name argument is required"), nil
	}
	body, ok := t.store.Body(name)
	if !ok {
		return jsonError(fmt.Sprintf("skill not found: %s", name)), nil
	}
	return body, nil
}
