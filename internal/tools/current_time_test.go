package tools

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

func fixedClock(ts string) func() time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

func TestCurrentTime_DefaultFormatIsRFC3339(t *testing.T) {
	tool := &currentTimeTool{now: fixedClock("2026-04-23T15:04:05Z")}
	out, err := tool.Invoke(context.Background(), "{}")
	if err != nil {
		t.Fatal(err)
	}
	if out != "2026-04-23T15:04:05Z" {
		t.Fatalf("out = %q", out)
	}
}

func TestCurrentTime_EmptyArgsDefaults(t *testing.T) {
	tool := &currentTimeTool{now: fixedClock("2026-04-23T15:04:05Z")}
	out, err := tool.Invoke(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "2026-04-23T15:04:05Z" {
		t.Fatalf("out = %q", out)
	}
}

func TestCurrentTime_CustomFormat(t *testing.T) {
	tool := &currentTimeTool{now: fixedClock("2026-04-23T15:04:05Z")}
	out, err := tool.Invoke(context.Background(), `{"format":"2006-01-02"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "2026-04-23" {
		t.Fatalf("out = %q", out)
	}
}

func TestCurrentTime_EmptyFormatStringFallsBack(t *testing.T) {
	tool := &currentTimeTool{now: fixedClock("2026-04-23T15:04:05Z")}
	out, err := tool.Invoke(context.Background(), `{"format":""}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "2026-04-23T15:04:05Z" {
		t.Fatalf("out = %q", out)
	}
}

func TestCurrentTime_UTCNotLocal(t *testing.T) {
	// Build a timezone-offset time and verify it is converted to UTC.
	tz := time.FixedZone("PDT", -7*3600)
	fixed := time.Date(2026, 4, 23, 8, 4, 5, 0, tz) // 15:04:05Z
	tool := &currentTimeTool{now: func() time.Time { return fixed }}
	out, err := tool.Invoke(context.Background(), "{}")
	if err != nil {
		t.Fatal(err)
	}
	if out != "2026-04-23T15:04:05Z" {
		t.Fatalf("expected UTC conversion, got %q", out)
	}
}

func TestCurrentTime_MalformedArgsError(t *testing.T) {
	tool := NewCurrentTime()
	out, err := tool.Invoke(context.Background(), `not json`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `{"error":"invalid arguments: `) {
		t.Fatalf("out = %q", out)
	}
}

func TestCurrentTime_LiveClockProducesValidRFC3339(t *testing.T) {
	tool := NewCurrentTime()
	out, err := tool.Invoke(context.Background(), "{}")
	if err != nil {
		t.Fatal(err)
	}
	// Must end in Z (UTC) and match the RFC3339 shape.
	rfc := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	if !rfc.MatchString(out) {
		t.Fatalf("out = %q", out)
	}
}
