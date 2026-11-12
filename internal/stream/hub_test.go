package stream

import (
	"strings"
	"sync"
	"testing"
)

func TestReplayAndEviction(t *testing.T) {
	h := New(3, 32768)
	for _, line := range []string{"one", "two", "three"} {
		h.Publish(Normalize("s", "h", "/log", line))
	}
	events, gap, _ := h.Read("", 0)
	if len(events) != 3 || gap {
		t.Fatal("initial snapshot", len(events), gap)
	}
	cursor := events[0].ID
	h.Publish(Normalize("s", "h", "/log", "four"))
	events, gap, _ = h.Read(cursor, 0)
	if gap || len(events) != 3 || events[0].Message != "two" {
		t.Fatal("cursor immediately before oldest must replay without gap")
	}
	h.Publish(Normalize("s", "h", "/log", "five"))
	events, gap, _ = h.Read(cursor, 2)
	if !gap || len(events) != 2 || events[0].Message != "three" {
		t.Fatal("evicted cursor must report a gap")
	}
	_, gap, _ = h.Read("previous-process:3", 0)
	if !gap {
		t.Fatal("restart must report gap")
	}
	_, gap, _ = h.Read("invalid", 0)
	if !gap {
		t.Fatal("malformed cursor must report gap")
	}
}
func TestByteBudgetAndConcurrentPublish(t *testing.T) {
	h := New(1000, 32768)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				h.Publish(Normalize("s", "host", "/log", strings.Repeat("a", 1000)))
				h.Read("", 10)
			}
		}()
	}
	wg.Wait()
	count, bytes := h.Stats()
	if count == 0 || count >= 400 || bytes > 32768 {
		t.Fatalf("unbounded buffer: count=%d bytes=%d", count, bytes)
	}
	events, _, _ := h.Read("", 0)
	seen := map[string]bool{}
	for _, e := range events {
		if seen[e.ID] {
			t.Fatal("duplicate event id")
		}
		seen[e.ID] = true
	}
}
func TestNormalize(t *testing.T) {
	e := Normalize("source", "host", "/log", `{"severity":"WARNING","message":"slow request","timestamp":"2026-11-06T10:00:00Z","duration":42}`)
	if e.Level != "warn" || e.Fields["duration"] != float64(42) || e.Timestamp == "" || e.SourceID != "source" {
		t.Fatalf("bad JSON event: %+v", e)
	}
	e = Normalize("s", "h", "/log", "2026-11-06T10:00:00Z ERROR connection lost")
	if e.Level != "error" || e.Timestamp == "" || e.Fields != nil {
		t.Fatalf("bad text event: %+v", e)
	}
}
