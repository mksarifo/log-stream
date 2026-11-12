// Package logging configures diagnostics and propagates log context to workers.
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode"
)

type Options struct {
	Level, Format, File string
	MaxBytes            int64
	Backups             int
}

func New(options Options, stderr io.Writer) (*slog.Logger, io.Closer, error) {
	var level slog.Level
	switch strings.ToLower(options.Level) {
	case "", "info":
		level = slog.LevelInfo
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, nil, errors.New("log-level must be debug, info, warn, or error")
	}
	format := strings.ToLower(options.Format)
	if format != "" && format != "text" && format != "json" {
		return nil, nil, errors.New("log-format must be text or json")
	}
	settings := &slog.HandlerOptions{Level: level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Value.Kind() == slog.KindDuration {
			return slog.String(a.Key, a.Value.Duration().String())
		}
		return a
	}}
	makeHandler := func(output io.Writer) slog.Handler {
		if format == "json" {
			return slog.NewJSONHandler(output, settings)
		}
		return slog.NewTextHandler(output, settings)
	}
	var output io.Writer = stderr
	var closer io.Closer = io.NopCloser(strings.NewReader(""))
	if options.File != "" {
		file, err := openRotating(options.File, options.MaxBytes, options.Backups)
		if err != nil {
			return nil, nil, fmt.Errorf("open diagnostic log: %w", err)
		}
		closer = file
		reporter := slog.New(makeHandler(stderr)).With("service", "logstream")
		output = &mirroredWriter{console: stderr, file: file, report: func(err error) {
			reporter.Error("logging.file_write_failed", "error", Clean(err.Error()), "stderr_continues", true)
		}}
	}
	handler := makeHandler(output)
	return slog.New(handler).With("service", "logstream"), closer, nil
}

// File failures must not silence stderr. Report once until writes recover.
type mirroredWriter struct {
	console io.Writer
	file    *rotatingWriter
	failed  bool
	report  func(error)
}

func (w *mirroredWriter) Write(p []byte) (int, error) {
	w.file.mu.Lock()
	defer w.file.mu.Unlock()
	n, err := w.console.Write(p)
	_, fileErr := w.file.writeLocked(p)
	if fileErr != nil && !w.failed {
		w.report(fileErr)
		w.failed = true
	}
	if fileErr == nil {
		w.failed = false
	}
	return n, err
}

type contextKey struct{}

func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}
func FromContext(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok {
		return logger
	}
	return fallback
}

// Callers must remove secrets before cleaning server-supplied diagnostics.
func Clean(message string) string {
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	runes := []rune(message)
	if len(runes) > 1024 {
		return string(runes[:1024]) + " … [truncated]"
	}
	return message
}
