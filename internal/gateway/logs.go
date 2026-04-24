package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"microagent2/internal/logstream"
)

// logLevels is the ordered level hierarchy. A request filtering by
// level="info" includes info, warn, error; filtering by "warn" includes
// warn+error; etc. Key order matches slog's default level names.
var logLevels = map[string]int{
	"DEBUG": 0,
	"INFO":  1,
	"WARN":  2,
	"ERROR": 3,
}

// logEntry is the shape returned to the dashboard. The raw JSON from the
// stream is passed through as RawJSON so custom fields survive intact;
// flat fields are broken out for the filter and display paths.
type logEntry struct {
	Time          string          `json:"time"`
	Level         string          `json:"level"`
	Service       string          `json:"service"`
	Msg           string          `json:"msg"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	RawJSON       json.RawMessage `json:"raw"`
	StreamID      string          `json:"stream_id"`
}

// handleListLogServices returns the list of services for which log
// streams exist in Valkey, merged with the gateway's own stream.
func (s *Server) handleListLogServices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := s.discoverLogStreams(ctx)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"services": out})
}

// discoverLogStreams enumerates log:<service> keys currently in Valkey.
// This catches both live and recently-dead services, since streams
// persist beyond a service's lifetime (bounded by MAXLEN).
func (s *Server) discoverLogStreams(ctx context.Context) []string {
	rdb := s.client.Redis()
	seen := map[string]bool{}
	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, "log:*", 100).Result()
		if err != nil {
			s.logger.Warn("log_services_scan_failed", "error", err.Error())
			break
		}
		for _, k := range keys {
			if svc := strings.TrimPrefix(k, "log:"); svc != "" {
				seen[svc] = true
			}
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	out := make([]string, 0, len(seen))
	for svc := range seen {
		out = append(out, svc)
	}
	sort.Strings(out)
	return out
}

// handleLogHistory reads recent entries from one or more streams,
// applies filters, merges by timestamp, and returns JSON.
func (s *Server) handleLogHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filters := parseLogFilters(r)
	services := filters.services
	if len(services) == 0 {
		services = s.discoverLogStreams(ctx)
	}

	limit := filters.limit
	if limit <= 0 {
		limit = 200
	}

	entries := make([]logEntry, 0, limit)
	for _, svc := range services {
		perStream := s.readStreamHistory(ctx, svc, filters, limit)
		entries = append(entries, perStream...)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Time < entries[j].Time
	})
	// Trim to the overall limit after merging; take the newest N.
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
}

// readStreamHistory reads up to `limit` entries from one stream,
// applying correlation_id/level/text filters.
func (s *Server) readStreamHistory(ctx context.Context, service string, f logFilters, limit int) []logEntry {
	stream := logstream.StreamName(service)
	rdb := s.client.Redis()
	// XRevRange returns newest first; we want newest regardless of merge order.
	raw, err := rdb.XRevRangeN(ctx, stream, "+", "-", int64(limit*4)).Result()
	if err != nil {
		if err != redis.Nil {
			s.logger.Warn("log_history_xrange_failed", "stream", stream, "error", err.Error())
		}
		return nil
	}
	out := make([]logEntry, 0, limit)
	for _, m := range raw {
		entry, ok := decodeStreamEntry(m, service)
		if !ok {
			continue
		}
		if !filterMatches(entry, f) {
			continue
		}
		out = append(out, entry)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// handleLogTail opens an SSE stream. Clients receive `data: <json>\n\n`
// per log entry matching their filters. Uses XRead BLOCK per stream
// concurrently; fans events into the single response writer serially.
func (s *Server) handleLogTail(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	filters := parseLogFilters(r)
	services := filters.services
	if len(services) == 0 {
		services = s.discoverLogStreams(r.Context())
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // hint for reverse proxies
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, ": tail-open")
	flusher.Flush()

	ctx := r.Context()
	events := make(chan logEntry, 32)

	// Per-stream reader goroutines. Each blocks on XRead; on entry, pushes
	// to the shared channel.
	for _, svc := range services {
		go s.tailStream(ctx, svc, events)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case e := <-events:
			if !filterMatches(e, filters) {
				continue
			}
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// tailStream runs an XRead BLOCK loop on one stream, pushing decoded
// entries into events until ctx is cancelled.
func (s *Server) tailStream(ctx context.Context, service string, events chan<- logEntry) {
	stream := logstream.StreamName(service)
	rdb := s.client.Redis()
	lastID := "$" // start from the newest entry
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		res, err := rdb.XRead(ctx, &redis.XReadArgs{
			Streams: []string{stream, lastID},
			Count:   32,
			Block:   5 * time.Second,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err == redis.Nil {
				continue
			}
			// Log briefly and back off; stream may not exist yet (service
			// hasn't logged anything since restart).
			time.Sleep(500 * time.Millisecond)
			continue
		}
		for _, sRes := range res {
			for _, m := range sRes.Messages {
				lastID = m.ID
				if e, ok := decodeStreamEntry(m, service); ok {
					select {
					case events <- e:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// decodeStreamEntry turns a raw Valkey stream message into a logEntry.
// Returns false if the message's json payload is missing or unparseable.
func decodeStreamEntry(m redis.XMessage, service string) (logEntry, bool) {
	raw, ok := m.Values["json"].(string)
	if !ok {
		return logEntry{}, false
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return logEntry{}, false
	}
	entry := logEntry{
		Service:  service,
		StreamID: m.ID,
		RawJSON:  json.RawMessage(raw),
	}
	if v, ok := parsed["time"].(string); ok {
		entry.Time = v
	}
	if v, ok := parsed["level"].(string); ok {
		entry.Level = v
	}
	if v, ok := parsed["msg"].(string); ok {
		entry.Msg = v
	}
	if v, ok := parsed["correlation_id"].(string); ok {
		entry.CorrelationID = v
	}
	return entry, true
}

// logFilters holds the parsed query-parameter filter criteria.
type logFilters struct {
	services      []string
	level         string
	correlationID string
	query         string
	limit         int
}

func parseLogFilters(r *http.Request) logFilters {
	q := r.URL.Query()
	f := logFilters{
		level:         strings.ToUpper(strings.TrimSpace(q.Get("level"))),
		correlationID: strings.TrimSpace(q.Get("correlation_id")),
		query:         strings.ToLower(strings.TrimSpace(q.Get("query"))),
	}
	if raw := q.Get("services"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				f.services = append(f.services, s)
			}
		}
	}
	if raw := q.Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			f.limit = n
		}
	}
	return f
}

// filterMatches returns true if e satisfies all populated filter criteria.
func filterMatches(e logEntry, f logFilters) bool {
	if f.level != "" {
		min, ok := logLevels[f.level]
		if ok {
			got, gotOk := logLevels[strings.ToUpper(e.Level)]
			if gotOk && got < min {
				return false
			}
		}
	}
	if f.correlationID != "" && !strings.HasPrefix(e.CorrelationID, f.correlationID) {
		return false
	}
	if f.query != "" {
		haystack := strings.ToLower(e.Msg) + " " + strings.ToLower(string(e.RawJSON))
		if !strings.Contains(haystack, f.query) {
			return false
		}
	}
	return true
}
