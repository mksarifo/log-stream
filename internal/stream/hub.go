// Package stream owns the bounded, process-local replay buffer.
package stream

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Event struct {
	ID         string         `json:"id"`
	SourceID   string         `json:"sourceId"`
	Host       string         `json:"host"`
	Path       string         `json:"path"`
	ReceivedAt time.Time      `json:"receivedAt"`
	Timestamp  string         `json:"timestamp,omitempty"`
	Level      string         `json:"level"`
	Message    string         `json:"message"`
	Fields     map[string]any `json:"fields,omitempty"`
}

type Hub struct {
	mu                          sync.RWMutex
	epoch                       string
	seq                         uint64
	ring                        []Event
	head, size, bytes, maxBytes int
}

func New(capacity, maxBytes int) *Hub {
	if capacity < 1 || maxBytes < 32768 {
		panic("invalid replay buffer limits")
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return &Hub{epoch: hex.EncodeToString(b), ring: make([]Event, capacity), maxBytes: maxBytes}
}

var levelPattern = regexp.MustCompile(`(?i)\b(trace|debug|info|warn(?:ing)?|error|fatal|panic)\b`)

func Normalize(source, host, path, line string) Event {
	e := Event{SourceID: source, Host: host, Path: path, ReceivedAt: time.Now().UTC(), Level: "info", Message: line}
	if json.Unmarshal([]byte(line), &e.Fields) == nil && e.Fields != nil {
		for _, key := range []string{"level", "severity", "log_level"} {
			if v, ok := e.Fields[key].(string); ok {
				e.Level = normalizeLevel(v)
				break
			}
		}
		for _, key := range []string{"timestamp", "time", "ts"} {
			if v, ok := e.Fields[key].(string); ok {
				e.Timestamp = v
				break
			}
		}
	} else {
		e.Fields = nil
		if v := levelPattern.FindString(line); v != "" {
			e.Level = normalizeLevel(v)
		}
		if first, _, ok := strings.Cut(line, " "); ok {
			if _, err := time.Parse(time.RFC3339Nano, first); err == nil {
				e.Timestamp = first
			}
		}
	}
	return e
}

func normalizeLevel(v string) string {
	switch strings.ToLower(v) {
	case "trace", "debug":
		return "debug"
	case "warn", "warning":
		return "warn"
	case "error", "fatal", "panic", "critical":
		return "error"
	default:
		return "info"
	}
}

func weight(e Event) int { return len(e.Message)*3 + len(e.Host) + len(e.Path) + 512 }

func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e.ID = fmt.Sprintf("%s:%d", h.epoch, h.seq)
	for h.size > 0 && (h.size == len(h.ring) || h.bytes+weight(e) > h.maxBytes) {
		h.bytes -= weight(h.ring[h.head])
		h.ring[h.head] = Event{}
		h.head = (h.head + 1) % len(h.ring)
		h.size--
	}
	if weight(e) > h.maxBytes {
		return
	}
	h.ring[(h.head+h.size)%len(h.ring)] = e
	h.size++
	h.bytes += weight(e)
}

// Read returns an atomic snapshot after cursor. Gaps include eviction and restarts.
func (h *Hub) Read(cursor string, limit int) ([]Event, bool, string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	latest := fmt.Sprintf("%s:%d", h.epoch, h.seq)
	var after uint64
	gap := false
	if cursor != "" {
		epoch, n, ok := strings.Cut(cursor, ":")
		var err error
		after, err = strconv.ParseUint(n, 10, 64)
		gap = !ok || err != nil || epoch != h.epoch || after > h.seq
		if gap {
			after = 0
		}
	}
	if h.size > 0 && cursor != "" && !gap {
		_, n, _ := strings.Cut(h.ring[h.head].ID, ":")
		oldest, _ := strconv.ParseUint(n, 10, 64)
		gap = after+1 < oldest
	}
	out := make([]Event, 0)
	for i := 0; i < h.size; i++ {
		e := h.ring[(h.head+i)%len(h.ring)]
		_, n, _ := strings.Cut(e.ID, ":")
		seq, _ := strconv.ParseUint(n, 10, 64)
		if seq > after {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, gap, latest
}

func (h *Hub) Stats() (int, int) { h.mu.RLock(); defer h.mu.RUnlock(); return h.size, h.bytes }
