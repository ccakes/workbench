package tui

import (
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/ccakes/workbench/internal/api"
	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/logbuf"
	"github.com/ccakes/workbench/internal/service"
	"github.com/ccakes/workbench/internal/spanbuf"
)

// remoteSession drives the TUI from a detached daemon over the control socket.
// It mirrors the daemon's state into local caches kept fresh by the subscribe
// stream, so the model's render-time reads never block on the socket.
type remoteSession struct {
	client *api.Client
	stop   func()

	mu         sync.RWMutex
	keys       []string
	snaps      map[string]service.Snapshot
	configs    map[string]*config.ServiceConfig
	logs       map[string]*logbuf.Buffer
	hasTracing bool
	spans      []spanbuf.Span
	svcMap     spanbuf.ServiceMapSnapshot
	statSpans  int
	statBytes  int64
	lastTrace  time.Time

	out       chan events.Event
	done      chan struct{}
	closeOnce sync.Once
}

const remoteLogBackfill = 1000

// NewRemoteSession attaches to a detached daemon over the control socket. It
// returns an error if the daemon is unreachable or another TUI is already
// attached (the single interactive-client rule).
func NewRemoteSession(client *api.Client) (Session, error) {
	return newRemoteSession(client)
}

func newRemoteSession(client *api.Client) (*remoteSession, error) {
	r := &remoteSession{
		client:  client,
		snaps:   map[string]service.Snapshot{},
		configs: map[string]*config.ServiceConfig{},
		logs:    map[string]*logbuf.Buffer{},
		out:     make(chan events.Event, 256),
		done:    make(chan struct{}),
	}

	// Status is core — fail the attach if we can't get it.
	if err := r.fetchStatus(); err != nil {
		return nil, err
	}
	r.fetchConfigs()
	r.backfillLogs()
	r.seedTracing()

	stream, stop, err := client.Subscribe()
	if err != nil {
		return nil, err
	}
	r.stop = stop
	go r.run(stream)
	return r, nil
}

// run consumes the event stream, updating caches before forwarding each event
// to the model so the next render sees fresh data.
func (r *remoteSession) run(stream <-chan events.Event) {
	defer close(r.out)
	for evt := range stream {
		switch evt.Type {
		case events.LogLine:
			if d, ok := evt.Data.(events.LogLineData); ok {
				r.logBuf(evt.Service).AddLine(evt.Timestamp, d.Stream.String(), d.Line)
			}
		case events.ServiceStateChanged:
			r.fetchOneStatus(evt.Service)
		case events.SpanBatchReceived:
			r.maybeRefreshTraces()
		}
		select {
		case r.out <- evt:
		case <-r.done:
			return
		}
	}
}

// --- cache reads (called from the model's render path) ---

func (r *remoteSession) Keys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.keys
}

func (r *remoteSession) Info(key string) (service.Snapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snap, ok := r.snaps[key]
	return snap, ok
}

func (r *remoteSession) Config(key string) *config.ServiceConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.configs[key]
}

func (r *remoteSession) Logs(key string) *logbuf.Buffer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.logs[key]
}

func (r *remoteSession) HasTracing() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hasTracing
}

func (r *remoteSession) Spans() []spanbuf.Span {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.spans
}

func (r *remoteSession) ServiceMap() spanbuf.ServiceMapSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.svcMap
}

func (r *remoteSession) TraceStats() (int, int64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.statSpans, r.statBytes
}

// SpansByTrace is triggered by a user action (opening the waterfall), so a
// synchronous fetch is acceptable.
func (r *remoteSession) SpansByTrace(id [16]byte) []spanbuf.Span {
	data, err := r.client.Call("spans", map[string]string{"trace_id": spanbuf.TraceIDHex(id)})
	if err != nil {
		return nil
	}
	return decodeSpans(data)
}

// --- control (called from the model's key handler) ---

func (r *remoteSession) StartService(key string) error {
	_, err := r.client.Call("start", map[string]string{"service": key})
	return err
}

func (r *remoteSession) StopService(key string) error {
	_, err := r.client.Call("stop", map[string]string{"service": key})
	return err
}

func (r *remoteSession) RestartService(key, reason string) error {
	_, err := r.client.Call("restart", map[string]string{"service": key, "reason": reason})
	return err
}

func (r *remoteSession) ToggleWatch(key string) bool {
	data, err := r.client.Call("toggle-watch", map[string]string{"service": key})
	if err != nil {
		return false
	}
	var res map[string]bool
	if json.Unmarshal(data, &res) != nil {
		return false
	}
	return res["watch_enabled"]
}

func (r *remoteSession) ClearLogs(key string) {
	if b := r.Logs(key); b != nil {
		b.Clear()
	}
	_, _ = r.client.Call("clear-logs", map[string]string{"service": key})
}

func (r *remoteSession) Down() error {
	_, err := r.client.Call("down", nil)
	return err
}

func (r *remoteSession) CanBackground() bool         { return true }
func (r *remoteSession) Events() <-chan events.Event { return r.out }

func (r *remoteSession) Close() {
	r.closeOnce.Do(func() {
		close(r.done)
		if r.stop != nil {
			r.stop()
		}
	})
}

// --- fetch helpers ---

func (r *remoteSession) fetchStatus() error {
	data, err := r.client.Call("status", nil)
	if err != nil {
		return err
	}
	var statuses []api.ServiceStatus
	if err := json.Unmarshal(data, &statuses); err != nil {
		return err
	}
	r.mu.Lock()
	r.keys = r.keys[:0]
	for _, s := range statuses {
		r.keys = append(r.keys, s.Key)
		r.snaps[s.Key] = snapFromStatus(s)
	}
	r.mu.Unlock()
	return nil
}

func (r *remoteSession) fetchOneStatus(key string) {
	if key == "" {
		return
	}
	data, err := r.client.Call("status", map[string]string{"service": key})
	if err != nil {
		return
	}
	var s api.ServiceStatus
	if json.Unmarshal(data, &s) != nil {
		return
	}
	r.mu.Lock()
	r.snaps[key] = snapFromStatus(s)
	r.mu.Unlock()
}

func (r *remoteSession) fetchConfigs() {
	data, err := r.client.Call("config", nil)
	if err != nil {
		return
	}
	var cfgs []api.ServiceConfigInfo
	if json.Unmarshal(data, &cfgs) != nil {
		return
	}
	r.mu.Lock()
	for _, c := range cfgs {
		r.configs[c.Key] = cfgFromInfo(c)
	}
	r.mu.Unlock()
}

func (r *remoteSession) backfillLogs() {
	data, err := r.client.Call("logs", map[string]any{"last": remoteLogBackfill})
	if err != nil {
		return
	}
	var lines []api.LogLine
	if json.Unmarshal(data, &lines) != nil {
		return
	}
	for _, l := range lines {
		ts, perr := time.Parse(time.RFC3339Nano, l.Timestamp)
		if perr != nil {
			ts = time.Now()
		}
		svc := l.Service
		if svc == "" {
			continue
		}
		r.logBuf(svc).AddLine(ts, l.Stream, l.Text)
	}
}

func (r *remoteSession) seedTracing() {
	if _, _, err := r.fetchTraceStats(); err != nil {
		return // tracing disabled
	}
	r.mu.Lock()
	r.hasTracing = true
	r.mu.Unlock()
	r.refreshTraces()
}

func (r *remoteSession) maybeRefreshTraces() {
	r.mu.RLock()
	tracing, last := r.hasTracing, r.lastTrace
	r.mu.RUnlock()
	if !tracing || time.Since(last) < 500*time.Millisecond {
		return
	}
	r.refreshTraces()
}

func (r *remoteSession) refreshTraces() {
	var spans []spanbuf.Span
	if data, err := r.client.Call("spans", map[string]any{}); err == nil {
		spans = decodeSpans(data)
	}
	smap := r.fetchServiceMap()
	cnt, bytes, _ := r.fetchTraceStats()
	r.mu.Lock()
	r.spans = spans
	r.svcMap = smap
	r.statSpans = cnt
	r.statBytes = bytes
	r.lastTrace = time.Now()
	r.mu.Unlock()
}

func (r *remoteSession) fetchServiceMap() spanbuf.ServiceMapSnapshot {
	data, err := r.client.Call("service-map", nil)
	if err != nil {
		return spanbuf.ServiceMapSnapshot{}
	}
	var edges []struct {
		From        string `json:"from"`
		To          string `json:"to"`
		CallCount   int    `json:"call_count"`
		ErrorCount  int    `json:"error_count"`
		AvgDuration string `json:"avg_duration"`
	}
	if json.Unmarshal(data, &edges) != nil {
		return spanbuf.ServiceMapSnapshot{}
	}
	out := spanbuf.ServiceMapSnapshot{Edges: make([]spanbuf.ServiceEdge, 0, len(edges))}
	for _, e := range edges {
		d, _ := time.ParseDuration(e.AvgDuration)
		out.Edges = append(out.Edges, spanbuf.ServiceEdge{
			From: e.From, To: e.To, CallCount: e.CallCount, ErrorCount: e.ErrorCount, AvgDuration: d,
		})
	}
	return out
}

func (r *remoteSession) fetchTraceStats() (int, int64, error) {
	data, err := r.client.Call("trace-stats", nil)
	if err != nil {
		return 0, 0, err
	}
	var ts api.TraceStats
	if err := json.Unmarshal(data, &ts); err != nil {
		return 0, 0, err
	}
	return ts.Spans, ts.Bytes, nil
}

func (r *remoteSession) logBuf(key string) *logbuf.Buffer {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.logs[key]
	if b == nil {
		b = logbuf.New(0)
		r.logs[key] = b
	}
	return b
}

// --- wire conversions ---

func snapFromStatus(s api.ServiceStatus) service.Snapshot {
	snap := service.Snapshot{
		Key:          s.Key,
		DisplayName:  s.DisplayName,
		Status:       service.ParseStatus(s.Status),
		PID:          s.PID,
		ExitCode:     s.ExitCode,
		RestartCount: s.RestartCount,
		LastRestart:  s.LastRestart,
		LastError:    s.LastError,
		WatchEnabled: s.WatchEnabled,
		ServiceType:  s.Type,
		Backend:      s.Backend,
		ContainerID:  s.ContainerID,
		Image:        s.Image,
		Ports:        s.Ports,
	}
	// Synthesize StartTime from the reported uptime so Snapshot.Uptime() keeps
	// ticking between status refreshes.
	if s.Uptime != "" {
		if d, err := time.ParseDuration(s.Uptime); err == nil && d > 0 {
			snap.StartTime = time.Now().Add(-d)
		}
	}
	return snap
}

func cfgFromInfo(ci api.ServiceConfigInfo) *config.ServiceConfig {
	c := &config.ServiceConfig{
		Group:   ci.Group,
		Dir:     ci.Dir,
		EnvFile: ci.EnvFile,
	}
	c.Restart.Policy = ci.Restart
	if ci.Type == "container" {
		c.Container = &config.ContainerConfig{Image: ci.Image}
	} else if ci.Command != "" {
		c.Command = &config.Command{Parts: []string{ci.Command}}
	}
	return c
}

func decodeSpans(data json.RawMessage) []spanbuf.Span {
	var ws []struct {
		TraceID      string `json:"trace_id"`
		SpanID       string `json:"span_id"`
		ParentSpanID string `json:"parent_span_id"`
		Name         string `json:"name"`
		ServiceName  string `json:"service_name"`
		Kind         string `json:"kind"`
		StartTime    string `json:"start_time"`
		EndTime      string `json:"end_time"`
		Duration     string `json:"duration"`
		Status       string `json:"status"`
		StatusMsg    string `json:"status_msg"`
	}
	if json.Unmarshal(data, &ws) != nil {
		return nil
	}
	out := make([]spanbuf.Span, 0, len(ws))
	for _, w := range ws {
		sp := spanbuf.Span{
			Name:        w.Name,
			ServiceName: w.ServiceName,
			Kind:        parseSpanKind(w.Kind),
			Status:      parseSpanStatus(w.Status),
			StatusMsg:   w.StatusMsg,
		}
		sp.TraceID = decodeTraceID(w.TraceID)
		sp.SpanID = decodeSpanID(w.SpanID)
		sp.ParentSpanID = decodeSpanID(w.ParentSpanID)
		sp.StartTime, _ = time.Parse(time.RFC3339Nano, w.StartTime)
		sp.EndTime, _ = time.Parse(time.RFC3339Nano, w.EndTime)
		sp.Duration, _ = time.ParseDuration(w.Duration)
		out = append(out, sp)
	}
	return out
}

func decodeTraceID(s string) [16]byte {
	var id [16]byte
	if b, err := hex.DecodeString(s); err == nil && len(b) == 16 {
		copy(id[:], b)
	}
	return id
}

func decodeSpanID(s string) [8]byte {
	var id [8]byte
	if b, err := hex.DecodeString(s); err == nil && len(b) == 8 {
		copy(id[:], b)
	}
	return id
}

func parseSpanKind(s string) spanbuf.SpanKind {
	switch s {
	case "internal":
		return spanbuf.SpanKindInternal
	case "server":
		return spanbuf.SpanKindServer
	case "client":
		return spanbuf.SpanKindClient
	case "producer":
		return spanbuf.SpanKindProducer
	case "consumer":
		return spanbuf.SpanKindConsumer
	default:
		return spanbuf.SpanKindUnspecified
	}
}

func parseSpanStatus(s string) spanbuf.SpanStatus {
	switch s {
	case "ok":
		return spanbuf.StatusOK
	case "error":
		return spanbuf.StatusError
	default:
		return spanbuf.StatusUnset
	}
}
