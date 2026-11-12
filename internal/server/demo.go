package server

import (
	"context"
	"fmt"
	"time"

	"logstream/internal/store"
	"logstream/internal/stream"
)

var demoHosts = []store.Host{
	{ID: "demo-api", Name: "api-production", Address: "10.0.1.12", Port: 22, Username: "deploy"},
	{ID: "demo-worker", Name: "worker-production", Address: "10.0.1.24", Port: 22, Username: "deploy"},
	{ID: "demo-edge", Name: "edge-production", Address: "10.0.1.8", Port: 22, Username: "deploy"},
}
var demoSources = []store.Source{
	{ID: "demo-api-log", HostID: "demo-api", Path: "/var/log/app/api.log"},
	{ID: "demo-worker-log", HostID: "demo-worker", Path: "/var/log/app/worker.log"},
	{ID: "demo-edge-log", HostID: "demo-edge", Path: "/var/log/nginx/access.log"},
}
var samples = []struct {
	host           int
	level, message string
}{
	{0, "info", "GET /v1/projects 200 12ms request_id=8f41c2"},
	{2, "info", "192.168.1.42 GET /api/health HTTP/1.1 200 0.002s"},
	{1, "info", "Job completed queue=notifications job_id=job_84a duration=142ms"},
	{0, "debug", "Database query completed table=projects rows=24 duration=3.2ms"},
	{0, "info", "POST /v1/events 201 8ms request_id=3a9df1"},
	{1, "warn", "Queue latency above threshold queue=exports latency=2840ms threshold=2000ms"},
	{2, "info", "192.168.1.18 GET /assets/app.js HTTP/1.1 304 0.001s"},
	{0, "error", "Upstream request failed service=payments status=503 retry_in=1000ms"},
	{1, "info", "Processing batch queue=events size=50 worker=worker-03"},
	{0, "info", "GET /v1/users/me 200 6ms request_id=ef213b"},
	{1, "debug", "Connection pool healthy active=4 idle=12 max=20"},
	{2, "warn", "Rate limit approaching client=192.168.1.51 requests=92 limit=100"},
	{0, "info", "Cache hit key=project:configuration ttl=240s"},
	{1, "info", "Scheduled task finished task=cleanup expired_sessions=18"},
	{0, "info", "Upstream connection restored service=payments latency=42ms"},
}

func (a *App) setDemo(enabled bool) {
	a.demoMu.Lock()
	defer a.demoMu.Unlock()
	if a.demo == enabled {
		return
	}
	if !enabled {
		a.demoCancel()
		<-a.demoDone
		a.demo = false
		a.demoCancel = nil
		a.logger.Info("demo.stopped")
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.demoCancel = cancel
	a.demoDone = make(chan struct{})
	a.demo = true
	a.logger.Info("demo.started", "sources", len(demoSources))
	emit := func(i int, t time.Time) {
		sample := samples[i%len(samples)]
		h := demoHosts[sample.host]
		s := demoSources[sample.host]
		line := fmt.Sprintf("%s %-5s %s", t.UTC().Format(time.RFC3339Nano), sample.level, sample.message)
		e := stream.Normalize(s.ID, h.Name, s.Path, line)
		e.ReceivedAt = t.UTC()
		a.Hub.Publish(e)
	}
	for i := 0; i < 72; i++ {
		emit(i, time.Now().Add(time.Duration(i-72)*400*time.Millisecond))
	}
	done := a.demoDone
	go func() {
		defer close(done)
		ticker := time.NewTicker(650 * time.Millisecond)
		defer ticker.Stop()
		i := 72
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				emit(i, t)
				i++
			}
		}
	}()
}
