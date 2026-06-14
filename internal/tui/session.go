package tui

import (
	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/logbuf"
	"github.com/ccakes/workbench/internal/service"
	"github.com/ccakes/workbench/internal/spanbuf"
	"github.com/ccakes/workbench/internal/supervisor"
)

// Session is the data + control surface the TUI model depends on. It abstracts
// over a direct in-process supervisor (localSession, used by `bench up`) and a
// remote control-socket client (remoteSession, used by `bench` attaching to a
// detached daemon), so the model code is identical in both modes.
type Session interface {
	// Service list and state.
	Keys() []string
	Info(key string) (service.Snapshot, bool)
	Config(key string) *config.ServiceConfig
	Logs(key string) *logbuf.Buffer
	ClearLogs(key string)

	// Control.
	StartService(key string) error
	StopService(key string) error
	RestartService(key, reason string) error
	ToggleWatch(key string) bool

	// Tracing (no-ops / empty when tracing is disabled).
	HasTracing() bool
	Spans() []spanbuf.Span
	SpansByTrace(id [16]byte) []spanbuf.Span
	ServiceMap() spanbuf.ServiceMapSnapshot
	TraceStats() (count int, bytes int64)

	// Lifecycle.
	Events() <-chan events.Event
	// Down stops every service and ends the session. For a foreground
	// localSession it is a no-op (teardown happens when the process exits).
	Down() error
	// CanBackground reports whether the session can keep running after the TUI
	// disconnects. True only for a remote (detached daemon) session.
	CanBackground() bool
	Close()
}

// localSession is a thin passthrough over a supervisor and span store, used when
// the TUI runs in-process (foreground `bench up`).
type localSession struct {
	sup   *supervisor.Supervisor
	store *spanbuf.Store
	ch    chan events.Event
}

// NewLocalSession builds an in-process session over a supervisor and span store,
// used by foreground `bench up`.
func NewLocalSession(sup *supervisor.Supervisor, store *spanbuf.Store) Session {
	return newLocalSession(sup, store)
}

func newLocalSession(sup *supervisor.Supervisor, store *spanbuf.Store) *localSession {
	return &localSession{
		sup:   sup,
		store: store,
		ch:    sup.Bus().Subscribe(256),
	}
}

func (l *localSession) Keys() []string { return l.sup.ServiceKeys() }

func (l *localSession) Info(key string) (service.Snapshot, bool) {
	info := l.sup.ServiceInfo(key)
	if info == nil {
		return service.Snapshot{}, false
	}
	return info.Snapshot(), true
}

func (l *localSession) Config(key string) *config.ServiceConfig { return l.sup.ServiceConfig(key) }
func (l *localSession) Logs(key string) *logbuf.Buffer          { return l.sup.ServiceLogs(key) }

func (l *localSession) ClearLogs(key string) {
	if b := l.sup.ServiceLogs(key); b != nil {
		b.Clear()
	}
}

func (l *localSession) StartService(key string) error { return l.sup.StartService(key) }
func (l *localSession) StopService(key string) error  { return l.sup.StopService(key) }
func (l *localSession) ToggleWatch(key string) bool   { return l.sup.ToggleWatch(key) }
func (l *localSession) RestartService(key, r string) error {
	return l.sup.RestartService(key, r)
}

func (l *localSession) HasTracing() bool { return l.store != nil }

func (l *localSession) Spans() []spanbuf.Span {
	if l.store == nil {
		return nil
	}
	return l.store.Spans()
}

func (l *localSession) SpansByTrace(id [16]byte) []spanbuf.Span {
	if l.store == nil {
		return nil
	}
	return l.store.SpansByTrace(id)
}

func (l *localSession) ServiceMap() spanbuf.ServiceMapSnapshot {
	if l.store == nil {
		return spanbuf.ServiceMapSnapshot{}
	}
	return l.store.ServiceMap()
}

func (l *localSession) TraceStats() (int, int64) {
	if l.store == nil {
		return 0, 0
	}
	return l.store.Len(), l.store.BytesUsed()
}

func (l *localSession) Events() <-chan events.Event { return l.ch }
func (l *localSession) Down() error                 { return nil }
func (l *localSession) CanBackground() bool         { return false }
func (l *localSession) Close()                      { l.sup.Bus().Unsubscribe(l.ch) }
