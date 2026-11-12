package collector

import (
	"context"
	"io"
	"time"

	"logstream/internal/stream"
)

const parserIdleTimeout = time.Second

// Keep the blocking SSH reader separate from the parser so a quiet file still
// publishes its last multiline event. Parser state remains on this goroutine.
func readEvents(ctx context.Context, r io.Reader, parser *stream.Parser, multiline bool, onLine func(string), emit func(stream.Event)) error {
	publish := func(events []stream.Event) {
		for _, event := range events {
			emit(event)
		}
	}
	if !multiline {
		return readLines(r, func(line string) {
			if ctx.Err() == nil {
				onLine(line)
				publish(parser.Push(line))
			}
		})
	}
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lines := make(chan string, 16)
	done := make(chan error, 1)
	go func() {
		err := readLines(r, func(line string) {
			select {
			case lines <- line:
			case <-readCtx.Done():
			}
		})
		done <- err
		close(lines)
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	lastLine := time.Now()
	for {
		select {
		case <-ctx.Done():
			publish(parser.Flush())
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				publish(parser.Flush())
				return <-done
			}
			lastLine = time.Now()
			onLine(line)
			publish(parser.Push(line))
		case now := <-ticker.C:
			if now.Sub(lastLine) >= parserIdleTimeout {
				publish(parser.Flush())
			}
		}
	}
}
