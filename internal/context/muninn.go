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
	Content  string  `json:"content"`
	Concept  string  `json:"concept"`
	Category string  `json:"category"`
	Score    float64 `json:"score"`
	Why      string  `json:"why"`
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
			Content:  item.Content,
			Concept:  item.Concept,
			Category: item.Concept,
			Score:    item.Score,
			Why:      item.Summary,
		}
	}
	return memories, nil
}

type engramRequest struct {
	Vault      string   `json:"vault"`
	Concept    string   `json:"concept"`
	Content    string   `json:"content"`
	Tags       []string `json:"tags"`
	Confidence float64  `json:"confidence"`
}

func (m *MuninnClient) Store(ctx context.Context, content, category string, keyTerms []string) error {
	url := fmt.Sprintf("http://%s/api/engrams", m.addr)

	body, err := json.Marshal(engramRequest{
		Vault:      m.vault,
		Concept:    category,
		Content:    content,
		Tags:       keyTerms,
		Confidence: m.storeConfidence,
	})
	if err != nil {
		return err
	}

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

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("muninndb returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
