// Package logstream fans a service's slog output to both stdout (via a
// delegate handler) and a per-service Valkey stream `log:<service_id>`.
// The stream is bounded (XADD MAXLEN ~) so storage is predictable; a
// publish-queue overflow drops entries rather than blocking stdout.
//
// Services adopt the wrapper with a one-line change to their slog.New
// call. If Valkey is unreachable or the queue is full, stdout logging
// continues unaffected.
package logstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultMaxLen       = 10_000
	defaultQueueSize    = 1000
	defaultMaxEntrySize = 16 * 1024
	defaultWarnInterval = 60 * time.Second
)

// Publisher owns the Valkey publish goroutine and the drop counter. One
// Publisher per service; the wrapper Handler references it.
type Publisher struct {
	rdb          *redis.Client
	stream       string
	maxLen       int64
	queue        chan []byte
	drops        atomic.Int64
	warnInterval time.Duration
	stderr       *slog.Logger

	// enabled — when false, Publisher.write is a no-op. Allows operators
	// to disable the Valkey fan-out via env without changing handler wiring.
	enabled bool
}

// Options configures a Publisher. Zero values select sensible defaults.
type Options struct {
	// ServiceID is the stream suffix — the stream name is log:<ServiceID>.
	ServiceID string
	// MaxLen is the XADD MAXLEN ~ argument. Defaults to 10_000.
	MaxLen int64
	// QueueSize is the async publish queue depth. Defaults to 1000.
	QueueSize int
	// MaxEntrySize is the byte cap per stream entry; larger entries are
	// truncated and annotated with a truncated_bytes field. Defaults to
	// 16 KB.
	MaxEntrySize int
	// Enabled gates whether Valkey fan-out happens. False turns the
	// Publisher into a pure passthrough (stdout only). Defaults to true.
	// Callers read LOG_STREAM_ENABLED env separately.
	Enabled *bool
	// WarnInterval is the minimum gap between drop warnings emitted to
	// stderr. Defaults to 60 seconds.
	WarnInterval time.Duration
}

// NewPublisher constructs a Publisher and starts its drain goroutine.
// Call Close to stop the drain cleanly on shutdown.
func NewPublisher(rdb *redis.Client, opts Options) *Publisher {
	if opts.MaxLen <= 0 {
		opts.MaxLen = defaultMaxLen
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultQueueSize
	}
	if opts.MaxEntrySize <= 0 {
		opts.MaxEntrySize = defaultMaxEntrySize
	}
	if opts.WarnInterval <= 0 {
		opts.WarnInterval = defaultWarnInterval
	}
	enabled := true
	if opts.Enabled != nil {
		enabled = *opts.Enabled
	}
	p := &Publisher{
		rdb:          rdb,
		stream:       StreamName(opts.ServiceID),
		maxLen:       opts.MaxLen,
		queue:        make(chan []byte, opts.QueueSize),
		warnInterval: opts.WarnInterval,
		stderr:       slog.New(slog.NewJSONHandler(os.Stderr, nil)),
		enabled:      enabled,
	}
	if enabled {
		go p.drain()
		go p.warnLoop()
	}
	return p
}

// StreamName returns the canonical per-service stream name.
func StreamName(serviceID string) string {
	return "log:" + serviceID
}

func (p *Publisher) drain() {
	ctx := context.Background()
	for body := range p.queue {
		// Fire-and-forget; log failures go to stderr directly to avoid
		// re-entering the slog pipeline.
		_, err := p.rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: p.stream,
			MaxLen: p.maxLen,
			Approx: true,
			Values: map[string]interface{}{"json": string(body)},
		}).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			// Valkey write failed. Record as a drop; do not retry (the
			// caller's log already made it to stdout, so no user-visible
			// impact, and retrying would compound back-pressure during
			// Valkey outages).
			p.drops.Add(1)
		}
	}
}

func (p *Publisher) warnLoop() {
	t := time.NewTicker(p.warnInterval)
	defer t.Stop()
	for range t.C {
		if n := p.drops.Swap(0); n > 0 {
			p.stderr.Warn("log_stream_drops", "count", n, "stream", p.stream)
		}
	}
}

// write enqueues a fully-rendered JSON log line for publication. If the
// queue is full or the publisher is disabled, the entry is dropped.
// Returns the drop count increment so callers can observe behavior in
// tests; in production the value is ignored.
func (p *Publisher) write(body []byte) int64 {
	if !p.enabled {
		return 0
	}
	// Enforce the per-entry size cap with truncation + annotation.
	if len(body) > defaultMaxEntrySize {
		body = truncateJSON(body, defaultMaxEntrySize)
	}
	select {
	case p.queue <- body:
		return 0
	default:
		p.drops.Add(1)
		return 1
	}
}

// Close stops the drain goroutine. Pending entries are lost. Safe to
// call multiple times.
func (p *Publisher) Close() {
	if !p.enabled {
		return
	}
	// Idempotent close; drain exits when the channel closes.
	defer func() { _ = recover() }()
	close(p.queue)
}

// Drops returns the current drop counter (not reset). Intended for tests.
func (p *Publisher) Drops() int64 { return p.drops.Load() }

// truncateJSON reduces body to at most max bytes while keeping it valid
// JSON by re-encoding with long string values trimmed. If body cannot be
// parsed, returns a small synthetic JSON object noting the issue.
func truncateJSON(body []byte, max int) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return []byte(fmt.Sprintf(`{"msg":"log_entry_unparseable","original_bytes":%d}`, len(body)))
	}
	origSize := len(body)
	// Shrink long string values until under the cap.
	for attempt := 0; attempt < 5; attempt++ {
		shortened := false
		for k, v := range m {
			if s, ok := v.(string); ok && len(s) > 1024 {
				m[k] = s[:1024] + "…"
				shortened = true
			}
		}
		if !shortened {
			break
		}
		if out, _ := json.Marshal(m); len(out) <= max {
			m["truncated_bytes"] = origSize - len(out)
			out, _ = json.Marshal(m)
			return out
		}
	}
	m["truncated_bytes"] = origSize
	m["msg"] = "log_entry_truncated"
	out, _ := json.Marshal(m)
	if len(out) > max {
		// Pathological; emit a minimal record.
		return []byte(fmt.Sprintf(`{"msg":"log_entry_truncated","truncated_bytes":%d}`, origSize))
	}
	return out
}

// Handler wraps an inner slog.Handler and also sends each rendered log
// record to a Publisher. The stdout path is the authoritative log — the
// Publisher is a best-effort fan-out.
type Handler struct {
	inner slog.Handler
	pub   *Publisher
	attrs []slog.Attr
	group string
}

// NewHandler wraps inner so every Handle call also enqueues a JSON copy
// of the record for publication to the service's Valkey stream.
func NewHandler(inner slog.Handler, pub *Publisher) *Handler {
	return &Handler{inner: inner, pub: pub}
}

// Enabled delegates to the inner handler. The publisher is
// inner-handler-gated: if the inner handler declines the record, we don't
// render it for the stream either.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle forwards the record to the inner handler then emits a JSON
// rendering of the record for stream publication. Stream publish failures
// are silent (the drop counter records them).
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if err := h.inner.Handle(ctx, r); err != nil {
		return err
	}
	if h.pub == nil {
		return nil
	}
	// Render the record as a JSON object similar to slog.JSONHandler's
	// output. Include the attrs/group state we were cloned with.
	body := h.renderJSON(r)
	if body != nil {
		h.pub.write(body)
	}
	return nil
}

// WithAttrs returns a new Handler that embeds the given attrs in every
// record it processes.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.inner = h.inner.WithAttrs(attrs)
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

// WithGroup returns a new Handler that nests subsequent attrs under the
// given group name.
func (h *Handler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.inner = h.inner.WithGroup(name)
	clone.group = name
	return &clone
}

// renderJSON emits a JSON line matching slog.JSONHandler's shape so the
// Valkey stream entry is the same as the stdout line for a given record.
// This is an intentional duplicate of the inner handler's work; reusing
// the inner handler would require intercepting its writer, which couples
// us to slog internals.
func (h *Handler) renderJSON(r slog.Record) []byte {
	m := map[string]any{
		"time":  r.Time.UTC().Format(time.RFC3339Nano),
		"level": r.Level.String(),
		"msg":   r.Message,
	}
	for _, a := range h.attrs {
		m[a.Key] = attrValue(a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = attrValue(a.Value)
		return true
	})
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}

func attrValue(v slog.Value) any {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindTime:
		return v.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindGroup:
		group := map[string]any{}
		for _, ga := range v.Group() {
			group[ga.Key] = attrValue(ga.Value)
		}
		return group
	default:
		return v.Any()
	}
}

// NewLogger is a convenience that wraps an inner slog.JSONHandler (to
// stdout) with the stream-publishing Handler and returns a *slog.Logger.
func NewLogger(serviceID string, rdb *redis.Client, opts Options) *slog.Logger {
	if opts.ServiceID == "" {
		opts.ServiceID = serviceID
	}
	inner := slog.NewJSONHandler(os.Stdout, nil)
	if rdb == nil || !publisherEnabledFromOpts(opts) {
		// No Valkey client or explicitly disabled — return a plain logger.
		return slog.New(inner)
	}
	pub := NewPublisher(rdb, opts)
	return slog.New(NewHandler(inner, pub))
}

func publisherEnabledFromOpts(opts Options) bool {
	if opts.Enabled == nil {
		return true
	}
	return *opts.Enabled
}

// OptionsFromEnv reads `LOG_STREAM_ENABLED` (default "true") and
// `LOG_STREAM_MAXLEN` (default 10_000) from the environment. Services
// call this from main.go so the log-stream knobs live in one place.
func OptionsFromEnv() Options {
	enabled := os.Getenv("LOG_STREAM_ENABLED") != "false"
	maxLen := int64(defaultMaxLen)
	if v := os.Getenv("LOG_STREAM_MAXLEN"); v != "" {
		var n int64
		_, err := fmt.Sscanf(v, "%d", &n)
		if err == nil && n > 0 {
			maxLen = n
		}
	}
	return Options{
		MaxLen:  maxLen,
		Enabled: &enabled,
	}
}
