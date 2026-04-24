package context

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Memory struct {
	Content  string  `json:"content"`
	Category string  `json:"category"`
	Score    float64 `json:"score"`
}

type MuninnClient struct {
	addr       string
	httpClient *http.Client
}

func NewMuninnClient(addr string) *MuninnClient {
	return &MuninnClient{
		addr:       addr,
		httpClient: &http.Client{},
	}
}

func (m *MuninnClient) Recall(ctx context.Context, query string, limit int) ([]Memory, error) {
	url := fmt.Sprintf("http://%s/recall", m.addr)
	body := fmt.Sprintf(`{"query":%q,"limit":%d}`, query, limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("muninndb returned %d: %s", resp.StatusCode, string(respBody))
	}

	var memories []Memory
	if err := json.NewDecoder(resp.Body).Decode(&memories); err != nil {
		return nil, err
	}
	return memories, nil
}

func (m *MuninnClient) Store(ctx context.Context, content, category string, keyTerms []string) error {
	url := fmt.Sprintf("http://%s/store", m.addr)

	payload := map[string]any{
		"content":   content,
		"category":  category,
		"key_terms": keyTerms,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

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
