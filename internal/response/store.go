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
	rdb *redis.Client
}

func NewStore(rdb *redis.Client) *Store {
	return &Store{rdb: rdb}
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
func (s *Store) GetSessionMessages(ctx context.Context, sessionID string) ([]messaging.ChatMsg, error) {
	responses, err := s.GetSessionHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	var msgs []messaging.ChatMsg
	for _, resp := range responses {
		for _, item := range resp.Input {
			role := item.Role
			if role == "" {
				role = "user"
			}
			content := ""
			switch c := item.Content.(type) {
			case string:
				content = c
			default:
				if c != nil {
					data, _ := json.Marshal(c)
					content = string(data)
				}
			}
			msgs = append(msgs, messaging.ChatMsg{Role: role, Content: content})
		}
		for _, out := range resp.Output {
			if out.Type == "message" && out.Role == "assistant" {
				var text string
				for _, part := range out.Content {
					if part.Type == "output_text" || part.Type == "text" {
						text += part.Text
					}
				}
				if text != "" {
					msgs = append(msgs, messaging.ChatMsg{Role: "assistant", Content: text})
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
