package stream

import (
	"os"
	"strings"
	"testing"
)

func parseFixture(t *testing.T, mode string) []Event {
	t.Helper()
	data, err := os.ReadFile("testdata/tx.log")
	if err != nil {
		t.Fatal(err)
	}
	p := NewParser(mode, "source", "host", "/app.log")
	var events []Event
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		events = append(events, p.Push(line)...)
	}
	return append(events, p.Flush()...)
}

func TestTXSampleWorkerPayloadsTransactionsAndPlainMessages(t *testing.T) {
	events := parseFixture(t, ParserTX)
	kinds := map[string]int{}
	payloads := 0
	for _, e := range events {
		if e.SourceID != "source" || e.Host != "host" || e.Path != "/app.log" || e.ReceivedAt.IsZero() {
			t.Fatal("source context lost")
		}
		kind, _ := e.Fields["kind"].(string)
		kinds[kind]++
		switch kind {
		case "worker":
			if e.Level != "debug" || e.Timestamp == "" || e.Fields["component"] != "EventWorker" || e.Fields["pid"] != int64(12345) {
				t.Fatalf("worker metadata missing in event %d", kinds[kind])
			}
			if e.Fields["message"] == "Request Body:" {
				payloads++
				body, ok := e.Fields["payload"].(map[string]any)
				if !ok || body["command"] != "AUTHENTICATE" || !strings.Contains(e.Message, "\n{") {
					t.Fatal("payload detached from its worker")
				}
			}
		case "transaction":
			if e.Fields["partial"] != false || e.Fields["status"] != "success" || e.Level != "info" || e.Fields["transaction_id"] == "" || e.Fields["server"] != "SampleServer" {
				t.Fatal("transaction summary not recognized")
			}
			steps, ok := e.Fields["steps"].([]map[string]any)
			if !ok || len(steps) != 6 || e.Fields["thread"] == nil || e.Fields["bucket"] != int64(1) || e.Fields["duration_ms"] == nil {
				t.Fatal("transaction details not extracted")
			}
			if !strings.Contains(e.Message, "Start Root") || !strings.Contains(e.Message, "Total duration:") {
				t.Fatal("original block lost")
			}
		}
	}
	if kinds["worker"] != 88 || kinds["transaction"] != 22 || payloads != 22 || kinds["message"] == 0 {
		t.Fatalf("incorrect sample framing: kinds=%v payloads=%d", kinds, payloads)
	}
	// The regular parser still emits exactly one event for each physical line.
	regular := parseFixture(t, ParserRegular)
	if len(regular) != 442 {
		t.Fatalf("regular parsing changed: %d events", len(regular))
	}
}

const transactionFixture = `23:59:59
--------------------
  Customer Id:example(amount:10,fee:0,product:payment)
  23:59:58  ->  23:59:59
  TX Thread Pool Example:Thread:7:Bucket:1
  TX Payment on Server (42:Payment API:1) Server Group Id: Main Connection, Start Root [id:sample-id]
    TX Check parameters(DB Worker on Server) done, waited:1ms, duration:2ms
  TX Payment on Server (42:Payment API)  Failed [id:sample-id], Total duration:3ms, waited:1ms
--------------------`

func TestTXFailurePartialAndTimeOnly(t *testing.T) {
	p := NewParser(ParserTX, "s", "h", "/log")
	var events []Event
	for _, line := range strings.Split(transactionFixture, "\n") {
		events = append(events, p.Push(line)...)
	}
	if len(events) != 1 || events[0].Level != "error" || events[0].Timestamp != "23:59:59" || events[0].Fields["duration_ms"] != float64(3) || events[0].Fields["flow_instance"] != "1" {
		t.Fatal("failed transaction or time-only timestamp parsed incorrectly")
	}
	if len(p.Flush()) != 0 {
		t.Fatal("closing delimiter left a duplicate record")
	}
	// Starting in the middle of a file or stopping before the summary is honest
	// about missing context, and never creates an invented date or success state.
	p = NewParser(ParserTX, "s", "h", "/log")
	p.Push("    TX Check parameters(DB Worker on Server) done, waited:1ms, duration:2ms")
	part := p.Flush()
	if len(part) != 1 || part[0].Fields["partial"] != true || part[0].Timestamp != "" || part[0].Fields["status"] != nil {
		t.Fatal("partial transaction misrepresented")
	}
}

func TestTXInterleavedWorkersAndFallback(t *testing.T) {
	p := NewParser(ParserTX, "s", "h", "/log")
	header := func(thread string) string {
		return "12:00:00 Decoder/EventWorker/TX Worker Pool (thread no:" + thread + ", pid:123) :: *** Debugging *** Request Body:"
	}
	p.Push(header("5"))
	p.Push(`{"command":"first"}`)
	first := p.Push(header("7"))
	p.Push(`{"command":"second"}`)
	second := p.Flush()
	if len(first) != 1 || first[0].Fields["thread"] != int64(5) || len(second) != 1 || second[0].Fields["thread"] != int64(7) || strings.Contains(second[0].Message, "first") {
		t.Fatal("worker records mixed between threads")
	}
	for _, mode := range []string{ParserRegular, ParserTX} {
		p = NewParser(mode, "s", "h", "/log")
		for _, line := range []string{`{"level":"warn","message":"regular JSON"}`, "2026-11-07T10:00:00Z ERROR regular text"} {
			out := p.Push(line)
			if len(out) != 1 || out[0].Message != line {
				t.Fatal("regular fallback lost an event")
			}
		}
	}
}

func TestTXBoundedRecordsAndParserIsolation(t *testing.T) {
	first := NewParser(ParserTX, "one", "h", "/one")
	second := NewParser(ParserTX, "two", "h", "/two")
	first.Push("12:00:00 Decoder/EventWorker/Pool (thread no:1, pid:1) :: *** Debugging *** Request Body:")
	second.Push("12:00:01 TX Link check successful")
	first.Push(`{"command":"one"}`)
	if out := second.Flush(); len(out) != 1 || strings.Contains(out[0].Message, "command") || out[0].SourceID != "two" {
		t.Fatal("source parser state leaked")
	}
	limited := false
	for i := 0; i < 1000; i++ {
		for _, e := range first.Push(strings.Repeat("x", 256)) {
			if len(e.Message) > MaxRecordBytes {
				t.Fatal("unbounded record")
			}
			if e.Fields["record_limit_reached"] == true {
				limited = true
			}
		}
	}
	if !limited {
		t.Fatal("record limit was not applied")
	}
	if out := first.Flush(); len(out) > 0 && len(out[0].Message) > MaxRecordBytes {
		t.Fatal("unbounded final record")
	}
	// A closing delimiter also participates in the byte bound.
	p := NewParser(ParserTX, "s", "h", "/log")
	p.Push("12:00:00")
	p.Push("--------------------")
	p.Push("  " + strings.Repeat("x", MaxRecordBytes-100))
	for _, event := range p.Push(strings.Repeat("-", MaxRecordBytes-1)) {
		if len(event.Message) > MaxRecordBytes {
			t.Fatal("closing delimiter bypassed record limit")
		}
	}
}

func TestTXMalformedPayloadIsKeptAndMarkedPartial(t *testing.T) {
	p := NewParser(ParserTX, "s", "h", "/log")
	header := "12:00:00 Decoder/EventWorker/Pool (thread no:2, pid:3) :: *** Debugging *** Request Body:"
	p.Push(header)
	p.Push(`{"truncated":`)
	result := p.Flush()
	if len(result) != 1 || result[0].Fields["partial"] != true || result[0].Fields["continuation"] != `{"truncated":` {
		t.Fatal("malformed payload lost or misrepresented")
	}
}
