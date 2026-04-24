package context

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Memory struct {
	ID       string  `json:"id"`
	Content  string  `json:"content"`
	Concept  string  `json:"concept"`
	Category string  `json:"category"`
	Score    float64 `json:"score"`
	Why      string  `json:"why"`
}

type StoredMemory struct {
	Concept    string
	Content    string
	Summary    string
	Tags       []string
	MemoryType string
	Confidence float64
}

type MuninnClient struct {
	addr            string
	apiKey          string
	httpClient      *http.Client
	vault           string
	recallThreshold float64
	maxHops         int
	storeConfidence float64
}

func NewMuninnClient(addr, apiKey string, vault string, recallThreshold float64, maxHops int, storeConfidence float64) *MuninnClient {
	return &MuninnClient{
		addr:            addr,
		apiKey:          apiKey,
		httpClient:      &http.Client{},
		vault:           vault,
		recallThreshold: recallThreshold,
		maxHops:         maxHops,
		storeConfidence: storeConfidence,
	}
}

func (m *MuninnClient) Vault() string { return m.vault }

type activateRequest struct {
	Vault      string   `json:"vault"`
	Context    []string `json:"context"`
	MaxResults int      `json:"max_results"`
	Threshold  float64  `json:"threshold"`
	MaxHops    int      `json:"max_hops"`
}

type activateResponse struct {
	Activations []activateResponseItem `json:"activations"`
}

type activateResponseItem struct {
	ID      string  `json:"id"`
	Score   float64 `json:"score"`
	Concept string  `json:"concept"`
	Content string  `json:"content"`
	Summary string  `json:"summary"`
}

func (m *MuninnClient) Recall(ctx context.Context, query string, limit int) ([]Memory, error) {
	url := fmt.Sprintf("http://%s/api/activate", m.addr)

	body, err := json.Marshal(activateRequest{
		Vault:      m.vault,
		Context:    []string{query},
		MaxResults: limit,
		Threshold:  m.recallThreshold,
		MaxHops:    m.maxHops,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("muninndb returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result activateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	memories := make([]Memory, len(result.Activations))
	for i, item := range result.Activations {
		memories[i] = Memory{
			ID:       item.ID,
			Content:  item.Content,
			Concept:  item.Concept,
			Category: item.Concept,
			Score:    item.Score,
			Why:      item.Summary,
		}
	}
	return memories, nil
}

// memoryTypeCodes maps Muninn's string memory_type names to the uint8 codes
// used in the REST write schema (internal/transport/mbp/types.go:89).
var memoryTypeCodes = map[string]uint8{
	"fact":        0,
	"decision":    1,
	"observation": 2,
	"preference":  3,
	"issue":       4,
	"task":        5,
	"procedure":   6,
	"event":       7,
	"goal":        8,
	"constraint":  9,
	"identity":    10,
	"reference":   11,
}

func (m *MuninnClient) Store(ctx context.Context, spec StoredMemory) error {
	url := fmt.Sprintf("http://%s/api/engrams", m.addr)

	payload := map[string]any{
		"vault":   m.vault,
		"concept": spec.Concept,
		"content": spec.Content,
		"tags":    spec.Tags,
	}
	if spec.Summary != "" {
		payload["summary"] = spec.Summary
	}
	if spec.MemoryType != "" {
		if code, ok := memoryTypeCodes[spec.MemoryType]; ok {
			payload["memory_type"] = code
		}
		// also set type_label with the string form; Muninn stores it alongside
		// the numeric code and returns it on Read
		payload["type_label"] = spec.MemoryType
	}
	if spec.Confidence != 0.0 {
		payload["confidence"] = spec.Confidence
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	return m.postJSON(ctx, url, body, http.StatusOK, http.StatusCreated)
}

type consolidateRequest struct {
	Vault         string   `json:"vault"`
	IDs           []string `json:"ids"`
	MergedContent string   `json:"merged_content"`
}

type consolidateResponse struct {
	ID       string   `json:"id"`
	Archived []string `json:"archived"`
}

func (m *MuninnClient) Consolidate(ctx context.Context, ids []string, mergedContent string) (string, error) {
	url := fmt.Sprintf("http://%s/api/consolidate", m.addr)

	body, err := json.Marshal(consolidateRequest{
		Vault:         m.vault,
		IDs:           ids,
		MergedContent: mergedContent,
	})
	if err != nil {
		return "", err
	}

	var out consolidateResponse
	if err := m.postJSONDecode(ctx, url, body, &out, http.StatusOK, http.StatusCreated); err != nil {
		return "", err
	}
	return out.ID, nil
}

type evolveRequest struct {
	Content string `json:"content"`
	Summary string `json:"summary,omitempty"`
	Vault   string `json:"vault"`
}

type evolveResponse struct {
	ID string `json:"id"`
}

func (m *MuninnClient) Evolve(ctx context.Context, id, newContent, newSummary string) (string, error) {
	url := fmt.Sprintf("http://%s/api/engrams/%s/evolve", m.addr, id)

	body, err := json.Marshal(evolveRequest{
		Content: newContent,
		Summary: newSummary,
		Vault:   m.vault,
	})
	if err != nil {
		return "", err
	}

	var out evolveResponse
	if err := m.postJSONDecode(ctx, url, body, &out, http.StatusOK, http.StatusCreated); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (m *MuninnClient) Delete(ctx context.Context, id string) error {
	url := fmt.Sprintf("http://%s/api/engrams/%s", m.addr, id)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("muninndb DELETE returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

type linkRequest struct {
	SourceID string  `json:"source_id"`
	TargetID string  `json:"target_id"`
	RelType  uint16  `json:"rel_type"`
	Weight   float64 `json:"weight"`
	Vault    string  `json:"vault"`
}

func (m *MuninnClient) Link(ctx context.Context, sourceID, targetID string, relType uint16, weight float64) error {
	url := fmt.Sprintf("http://%s/api/link", m.addr)

	body, err := json.Marshal(linkRequest{
		SourceID: sourceID,
		TargetID: targetID,
		RelType:  relType,
		Weight:   weight,
		Vault:    m.vault,
	})
	if err != nil {
		return err
	}

	return m.postJSON(ctx, url, body, http.StatusOK, http.StatusCreated)
}

func (m *MuninnClient) postJSON(ctx context.Context, url string, body []byte, okStatuses ...int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	for _, s := range okStatuses {
		if resp.StatusCode == s {
			return nil
		}
	}
	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("muninndb POST %s returned %d: %s", url, resp.StatusCode, string(respBody))
}

func (m *MuninnClient) postJSONDecode(ctx context.Context, url string, body []byte, out any, okStatuses ...int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	ok := false
	for _, s := range okStatuses {
		if resp.StatusCode == s {
			ok = true
			break
		}
	}
	if !ok {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("muninndb POST %s returned %d: %s", url, resp.StatusCode, string(respBody))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
