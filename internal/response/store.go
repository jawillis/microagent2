package response

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"microagent2/internal/messaging"
	"github.com/oklog/ulid/v2"
	"github.com/redis/go-redis/v9"
)

type Status string

const (
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusInProgress Status = "in_progress"
)

type InputItem struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content any    `json:"content,omitempty"`
	CallID  string `json:"call_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Args    string `json:"arguments,omitempty"`
	Output  string `json:"output,omitempty"`
}

type OutputItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role,omitempty"`
	Content []ContentPart `json:"content,omitempty"`
	CallID  string        `json:"call_id,omitempty"`
	Name    string        `json:"name,omitempty"`
	Args    string        `json:"arguments,omitempty"`
	Output  string        `json:"output,omitempty"`
}

type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Response struct {
	ID                 string       `json:"id"`
	Input              []InputItem  `json:"input"`
	Output             []OutputItem `json:"output"`
	PreviousResponseID string       `json:"previous_response_id"`
	SessionID          string       `json:"session_id"`
	Model              string       `json:"model"`
	CreatedAt          string       `json:"created_at"`
	Status             Status       `json:"status"`
}

type Store struct {
	rdb             *redis.Client
	sessionHashTTL time.Duration
}

const defaultSessionHashTTL = 24 * time.Hour

func NewStore(rdb *redis.Client) *Store {
	return &Store{rdb: rdb, sessionHashTTL: defaultSessionHashTTL}
}

// NewStoreWithSessionHashTTL is like NewStore but lets the caller override
// the TTL applied to session_hash:* index entries. A non-positive value
// falls back to the default.
func NewStoreWithSessionHashTTL(rdb *redis.Client, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = defaultSessionHashTTL
	}
	return &Store{rdb: rdb, sessionHashTTL: ttl}
}

func sessionHashKey(hashHex string) string {
	return fmt.Sprintf("session_hash:%s", hashHex)
}

func NewResponseID() string {
	return "resp_" + ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

func NewSessionID() string {
	return uuid.New().String()
}

func responseKey(id string) string {
	return fmt.Sprintf("response:%s", id)
}

func sessionResponsesKey(sessionID string) string {
	return fmt.Sprintf("session:%s:responses", sessionID)
}

// StoreSessionPrefixHash records that a given conversation hash maps to a
// session id. The write expires after the store's configured TTL.
func (s *Store) StoreSessionPrefixHash(ctx context.Context, hashHex, sessionID string) error {
	return s.rdb.Set(ctx, sessionHashKey(hashHex), sessionID, s.sessionHashTTL).Err()
}

// LookupSessionByPrefixHash returns the session id recorded for a given
// conversation hash. ok=false (with nil error) means the hash is not indexed.
func (s *Store) LookupSessionByPrefixHash(ctx context.Context, hashHex string) (string, bool, error) {
	sid, err := s.rdb.Get(ctx, sessionHashKey(hashHex)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return sid, true, nil
}

// GetLastResponseID returns the most recent response id in a session, or
// "" (with nil error) if the session has no responses.
func (s *Store) GetLastResponseID(ctx context.Context, sessionID string) (string, error) {
	id, err := s.rdb.LIndex(ctx, sessionResponsesKey(sessionID), -1).Result()
	if err == redis.Nil {
		return "", nil
	}
	return id, err
}

func (s *Store) Save(ctx context.Context, resp *Response) error {
	inputJSON, err := json.Marshal(resp.Input)
	if err != nil {
		return err
	}
	outputJSON, err := json.Marshal(resp.Output)
	if err != nil {
		return err
	}

	key := responseKey(resp.ID)
	fields := map[string]any{
		"id":                   resp.ID,
		"input":                string(inputJSON),
		"output":               string(outputJSON),
		"previous_response_id": resp.PreviousResponseID,
		"session_id":           resp.SessionID,
		"model":                resp.Model,
		"created_at":           resp.CreatedAt,
		"status":               string(resp.Status),
	}

	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, key, fields)
	pipe.RPush(ctx, sessionResponsesKey(resp.SessionID), resp.ID)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *Store) Get(ctx context.Context, id string) (*Response, error) {
	vals, err := s.rdb.HGetAll(ctx, responseKey(id)).Result()
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, nil
	}
	return decodeResponse(vals)
}

func decodeResponse(vals map[string]string) (*Response, error) {
	resp := &Response{
		ID:                 vals["id"],
		PreviousResponseID: vals["previous_response_id"],
		SessionID:          vals["session_id"],
		Model:              vals["model"],
		CreatedAt:          vals["created_at"],
		Status:             Status(vals["status"]),
	}
	if v := vals["input"]; v != "" {
		if err := json.Unmarshal([]byte(v), &resp.Input); err != nil {
			return nil, err
		}
	}
	if v := vals["output"]; v != "" {
		if err := json.Unmarshal([]byte(v), &resp.Output); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// WalkChain traverses the response chain backward from the given ID to root,
// returning responses in chronological order (root first).
func (s *Store) WalkChain(ctx context.Context, id string) ([]*Response, error) {
	var chain []*Response
	seen := make(map[string]bool)
	current := id

	for current != "" {
		if seen[current] {
			return nil, fmt.Errorf("cycle detected in response chain at %s", current)
		}
		seen[current] = true

		resp, err := s.Get(ctx, current)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, fmt.Errorf("response not found: %s", current)
		}

		chain = append(chain, resp)
		current = resp.PreviousResponseID
	}

	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// ResolveChainMessages reconstructs the full conversation from a response chain as input/output pairs.
func (s *Store) ResolveChainMessages(ctx context.Context, id string) ([]InputItem, []OutputItem, error) {
	chain, err := s.WalkChain(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	var allInput []InputItem
	var allOutput []OutputItem
	for _, resp := range chain {
		allInput = append(allInput, resp.Input...)
		allOutput = append(allOutput, resp.Output...)
	}
	return allInput, allOutput, nil
}

// GetSessionHistory reads all response IDs from the session index, batch-reads
// response hashes, and returns them in order.
func (s *Store) GetSessionHistory(ctx context.Context, sessionID string) ([]*Response, error) {
	ids, err := s.rdb.LRange(ctx, sessionResponsesKey(sessionID), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	responses := make([]*Response, 0, len(ids))
	for _, id := range ids {
		resp, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			continue
		}
		responses = append(responses, resp)
	}
	return responses, nil
}

// GetSessionMessages returns the conversation for a session as ChatMsg pairs,
// suitable for feeding directly into context assembly or retro job processing.
// function_call / function_call_output items are preserved as assistant
// messages with tool_calls and tool role messages with tool_call_id.
func (s *Store) GetSessionMessages(ctx context.Context, sessionID string) ([]messaging.ChatMsg, error) {
	responses, err := s.GetSessionHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	var msgs []messaging.ChatMsg
	for _, resp := range responses {
		for _, item := range resp.Input {
			if item.Type == "function_call_output" {
				msgs = append(msgs, messaging.ChatMsg{
					Role:       "tool",
					Content:    item.Output,
					ToolCallID: item.CallID,
				})
				continue
			}
			role := item.Role
			if role == "" {
				role = "user"
			}
			content := FlattenContent(item.Content)
			if content == "" {
				continue
			}
			msgs = append(msgs, messaging.ChatMsg{Role: role, Content: content})
		}
		for _, out := range resp.Output {
			switch out.Type {
			case "function_call":
				msgs = append(msgs, messaging.ChatMsg{
					Role: "assistant",
					ToolCalls: []messaging.ToolCall{{
						ID:   out.CallID,
						Type: "function",
						Function: messaging.ToolCallFunction{
							Name:      out.Name,
							Arguments: out.Args,
						},
					}},
				})
			case "function_call_output":
				msgs = append(msgs, messaging.ChatMsg{
					Role:       "tool",
					Content:    out.Output,
					ToolCallID: out.CallID,
				})
			case "message":
				if out.Role == "assistant" {
					text := FlattenContent(out.Content)
					if text != "" {
						msgs = append(msgs, messaging.ChatMsg{Role: "assistant", Content: text})
					}
				}
			}
		}
	}
	return msgs, nil
}

// InheritSessionID reads the session_id from a previous response.
func (s *Store) InheritSessionID(ctx context.Context, previousResponseID string) (string, error) {
	sid, err := s.rdb.HGet(ctx, responseKey(previousResponseID), "session_id").Result()
	if err == redis.Nil {
		return "", fmt.Errorf("response not found: %s", previousResponseID)
	}
	return sid, err
}

// DeleteSession removes all response hashes referenced by the session's response list,
// the response list key, and session metadata.
func (s *Store) DeleteSession(ctx context.Context, sessionID string) error {
	listKey := sessionResponsesKey(sessionID)
	ids, err := s.rdb.LRange(ctx, listKey, 0, -1).Result()
	if err != nil {
		return err
	}

	pipe := s.rdb.Pipeline()
	for _, id := range ids {
		pipe.Del(ctx, responseKey(id))
	}
	pipe.Del(ctx, listKey)
	_, err = pipe.Exec(ctx)
	return err
}

// SessionExists checks if a session has any responses.
func (s *Store) SessionExists(ctx context.Context, sessionID string) (bool, error) {
	n, err := s.rdb.Exists(ctx, sessionResponsesKey(sessionID)).Result()
	return n > 0, err
}

// ListSessions scans for all session response lists and returns session summaries.
type SessionSummary struct {
	SessionID  string `json:"session_id"`
	TurnCount  int    `json:"turn_count"`
	LastActive string `json:"last_active"`
}

func (s *Store) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	var sessions []SessionSummary
	var cursor uint64

	for {
		keys, next, err := s.rdb.Scan(ctx, cursor, "session:*:responses", 100).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			parts := splitSessionKey(key)
			if parts == "" {
				continue
			}
			count, _ := s.rdb.LLen(ctx, key).Result()

			lastActive := ""
			lastID, err := s.rdb.LIndex(ctx, key, -1).Result()
			if err == nil && lastID != "" {
				ts, _ := s.rdb.HGet(ctx, responseKey(lastID), "created_at").Result()
				lastActive = ts
			}

			sessions = append(sessions, SessionSummary{
				SessionID:  parts,
				TurnCount:  int(count),
				LastActive: lastActive,
			})
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}

	if sessions == nil {
		sessions = []SessionSummary{}
	}
	return sessions, nil
}

func splitSessionKey(key string) string {
	// key format: session:{id}:responses
	if len(key) < len("session::responses") {
		return ""
	}
	prefix := "session:"
	suffix := ":responses"
	if key[:len(prefix)] != prefix || key[len(key)-len(suffix):] != suffix {
		return ""
	}
	return key[len(prefix) : len(key)-len(suffix)]
}
