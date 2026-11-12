package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"logstream/internal/logging"
	"logstream/internal/server"
	"logstream/internal/store"
)

func main() { os.Exit(run()) }

func run() int {
	addr := flag.String("addr", "127.0.0.1:8080", "Loopback listen address")
	data := flag.String("data", "data", "Private configuration directory")
	dist := flag.String("web", "web/dist", "Built React frontend directory")
	level := flag.String("log-level", "debug", "Diagnostic level: debug, info, warn, error")
	format := flag.String("log-format", "text", "Diagnostic format: text or json")
	file := flag.String("log-file", "", "Optional diagnostic file; stderr remains enabled")
	size := flag.Int("log-max-size", 10, "Maximum log file size in MiB (one record may exceed it)")
	backups := flag.Int("log-backups", 3, "Number of rotated diagnostic files to retain (1-100)")
	flag.Parse()
	if *size < 1 || *size > 1024 {
		fmt.Fprintln(os.Stderr, "logstream: log-max-size must be between 1 and 1024 MiB")
		return 1
	}
	logger, sink, err := logging.New(logging.Options{Level: *level, Format: *format, File: *file, MaxBytes: int64(*size) * 1024 * 1024, Backups: *backups}, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logstream:", err)
		return 1
	}
	defer sink.Close()
	logger = logger.With("process_id", os.Getpid())
	slog.SetDefault(logger)
	fatal := func(stage string, err error) int {
		logger.Error("app.start_failed", "stage", stage, "error", logging.Clean(err.Error()))
		return 1
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return fatal("configuration", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fatal("configuration", fmt.Errorf("only a loopback IP address is supported"))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	started := time.Now()
	logger.Info("app.starting", "address", *addr, "log_level", *level, "log_format", *format, "log_file", *file)
	db, err := store.Open(filepath.Join(*data, "logstream.db"))
	if err != nil {
		return fatal("database", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("store.close_failed", "error", logging.Clean(err.Error()))
		}
	}()
	hosts, sources, err := db.List()
	if err != nil {
		return fatal("database", err)
	}
	logger.Info("store.opened", "hosts", len(hosts), "sources", len(sources), "auto_reconnect", false)
	if _, err := os.Stat(filepath.Join(*dist, "index.html")); err != nil {
		logger.Warn("frontend.not_built", "directory", *dist, "hint", "build web assets or use the Vite development server")
	}
	app := server.New(ctx, db)
	defer app.Close()
	srv := &http.Server{Addr: *addr, Handler: app.Handler(*dist), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024, ErrorLog: slog.NewLogLogger(logger.With("component", "http").Handler(), slog.LevelError)}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fatal("listen", err)
	}
	logger.Info("app.ready", "url", "http://"+listener.Addr().String(), "startup_duration", time.Since(started))
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	select {
	case err = <-served:
		cancel()
		if err != nil && err != http.ErrServerClosed {
			logger.Error("http.serve_failed", "error", logging.Clean(err.Error()))
			return 1
		}
	case <-ctx.Done():
		logger.Info("app.shutdown_started")
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		err = srv.Shutdown(shutdownCtx)
		stop()
		if err != nil {
			logger.Error("http.shutdown_failed", "error", logging.Clean(err.Error()))
			srv.Close()
		}
		<-served
	}
	app.Close()
	logger.Info("app.stopped", "uptime", time.Since(started))
	return 0
}
