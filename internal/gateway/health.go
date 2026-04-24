package gateway

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type ServiceHealth struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func checkValkey(ctx context.Context, s *Server) ServiceHealth {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := s.client.Ping(ctx); err != nil {
		return ServiceHealth{Name: "valkey", Status: "unhealthy", Message: err.Error()}
	}
	return ServiceHealth{Name: "valkey", Status: "healthy"}
}

func checkHTTPService(ctx context.Context, name, url string, timeout time.Duration) ServiceHealth {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ServiceHealth{Name: name, Status: "unhealthy", Message: err.Error()}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ServiceHealth{Name: name, Status: "unhealthy", Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return ServiceHealth{Name: name, Status: "healthy"}
	}
	return ServiceHealth{Name: name, Status: "unhealthy", Message: fmt.Sprintf("HTTP %d", resp.StatusCode)}
}
