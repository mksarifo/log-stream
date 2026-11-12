package collector

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"logstream/internal/stream"
)

func TestTXParserFlushesQuietFileAndCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); reader.Close(); writer.Close() })
	events := make(chan stream.Event, 4)
	done := make(chan error, 1)
	go func() {
		done <- readEvents(ctx, reader, stream.NewParser(stream.ParserTX, "s", "h", "/log"), true, func(string) {}, func(e stream.Event) { events <- e })
	}()
	io.WriteString(writer, "12:00:00 Decoder/EventWorker/Pool (thread no:2, pid:3) :: *** Debugging *** Request Body:\n{\"command\":\"test\"}\n")
	select {
	case event := <-events:
		if event.Level != "debug" || event.Fields["payload"] == nil || !strings.Contains(event.Message, "command") {
			t.Fatal("idle flush lost payload")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("quiet file never published its last record")
	}
	io.WriteString(writer, "12:00:01 TX State Writer writing sample.FINAL\n")
	cancel()
	writer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("parser did not stop")
	}
}

func TestTXParserFlushesEOFAndRegularRemainsImmediate(t *testing.T) {
	for _, mode := range []string{stream.ParserRegular, stream.ParserTX} {
		var events []stream.Event
		data := "12:00:00 Decoder/EventWorker/Pool (thread no:2, pid:3) :: *** Debugging *** Request Body:\n{\"command\":\"test\"}\n"
		err := readEvents(context.Background(), strings.NewReader(data), stream.NewParser(mode, "s", "h", "/log"), mode == stream.ParserTX, func(string) {}, func(e stream.Event) { events = append(events, e) })
		if err != nil {
			t.Fatal(err)
		}
		if (mode == stream.ParserRegular && len(events) != 2) || (mode == stream.ParserTX && (len(events) != 1 || events[0].Fields["payload"] == nil)) {
			t.Fatal("EOF framing incorrect")
		}
	}
}
