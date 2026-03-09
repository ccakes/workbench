package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ccakes/workbench/internal/logbuf"
	"github.com/ccakes/workbench/internal/spanbuf"
)

func (s *Server) handlePing(_ json.RawMessage) (any, error) {
	return map[string]string{"version": s.version}, nil
}

func (s *Server) getStore() *spanbuf.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store
}

type statusParams struct {
	Service string `json:"service"`
}

// ServiceStatus is the wire format for a service status response. Exported for CLI reuse.
type ServiceStatus struct {
	Key          string   `json:"key"`
	DisplayName  string   `json:"display_name"`
	Status       string   `json:"status"`
	Type         string   `json:"type"`
	PID          int      `json:"pid,omitempty"`
	ContainerID  string   `json:"container_id,omitempty"`
	Image        string   `json:"image,omitempty"`
	Uptime       string   `json:"uptime,omitempty"`
	ExitCode     int      `json:"exit_code,omitempty"`
	RestartCount int      `json:"restart_count"`
	LastRestart  string   `json:"last_restart,omitempty"`
	LastError    string   `json:"last_error,omitempty"`
	WatchEnabled bool     `json:"watch_enabled"`
	Ports        []string `json:"ports,omitempty"`
}

func (s *Server) handleStatus(raw json.RawMessage) (any, error) {
	var p statusParams
	_ = json.Unmarshal(raw, &p)

	if p.Service != "" {
		info := s.sup.ServiceInfo(p.Service)
		if info == nil {
			return nil, fmt.Errorf("unknown service %q", p.Service)
		}
		return s.buildServiceStatus(p.Service), nil
	}

	keys := s.sup.ServiceKeys()
	result := make([]ServiceStatus, 0, len(keys))
	for _, key := range keys {
		result = append(result, s.buildServiceStatus(key))
	}
	return result, nil
}

func (s *Server) buildServiceStatus(key string) ServiceStatus {
	info := s.sup.ServiceInfo(key)
	if info == nil {
		return ServiceStatus{Key: key}
	}
	snap := info.Snapshot()
	st := ServiceStatus{
		Key:          snap.Key,
		DisplayName:  snap.Name(),
		Status:       snap.Status.String(),
		Type:         snap.ServiceType,
		PID:          snap.PID,
		ContainerID:  snap.ContainerID,
		Image:        snap.Image,
		ExitCode:     snap.ExitCode,
		RestartCount: snap.RestartCount,
		LastRestart:  snap.LastRestart,
		LastError:    snap.LastError,
		WatchEnabled: snap.WatchEnabled,
		Ports:        snap.Ports,
	}
	uptime := snap.Uptime()
	if uptime > 0 {
		st.Uptime = uptime.String()
	}
	return st
}

type serviceParam struct {
	Service string `json:"service"`
}

func (s *Server) handleStart(raw json.RawMessage) (any, error) {
	var p serviceParam
	if err := json.Unmarshal(raw, &p); err != nil || p.Service == "" {
		return nil, fmt.Errorf("service parameter required")
	}
	if err := s.sup.StartService(p.Service); err != nil {
		return nil, err
	}
	return map[string]string{"status": "started"}, nil
}

func (s *Server) handleStop(raw json.RawMessage) (any, error) {
	var p serviceParam
	if err := json.Unmarshal(raw, &p); err != nil || p.Service == "" {
		return nil, fmt.Errorf("service parameter required")
	}
	if err := s.sup.StopService(p.Service); err != nil {
		return nil, err
	}
	return map[string]string{"status": "stopped"}, nil
}

type restartParams struct {
	Service string `json:"service"`
	Reason  string `json:"reason"`
}

func (s *Server) handleRestart(raw json.RawMessage) (any, error) {
	var p restartParams
	if err := json.Unmarshal(raw, &p); err != nil || p.Service == "" {
		return nil, fmt.Errorf("service parameter required")
	}
	reason := p.Reason
	if reason == "" {
		reason = "api restart"
	}
	if err := s.sup.RestartService(p.Service, reason); err != nil {
		return nil, err
	}
	return map[string]string{"status": "restarted"}, nil
}

type logsParams struct {
	Service  string `json:"service"`
	Last     int    `json:"last"`
	AfterSeq uint64 `json:"after_seq"` // sequence cursor — return only lines with seq > this value
}

// LogLine is the wire format for a log line. Exported for CLI reuse.
type LogLine struct {
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Text      string `json:"text"`
	Seq       uint64 `json:"seq"`
}

func (s *Server) handleLogs(raw json.RawMessage) (any, error) {
	var p logsParams
	if err := json.Unmarshal(raw, &p); err != nil || p.Service == "" {
		return nil, fmt.Errorf("service parameter required")
	}
	n := p.Last
	if n <= 0 {
		n = 100
	}
	buf := s.sup.ServiceLogs(p.Service)
	if buf == nil {
		return nil, fmt.Errorf("unknown service %q", p.Service)
	}

	var lines []logbuf.Line
	if p.AfterSeq > 0 {
		lines = buf.LastAfter(p.AfterSeq, n)
	} else {
		lines = buf.Last(n)
	}

	result := make([]LogLine, len(lines))
	for i, l := range lines {
		result[i] = LogLine{
			Timestamp: l.Timestamp.Format(time.RFC3339Nano),
			Stream:    l.Stream,
			Text:      l.Text,
			Seq:       l.Seq,
		}
	}
	return result, nil
}

func (s *Server) handleToggleWatch(raw json.RawMessage) (any, error) {
	var p serviceParam
	if err := json.Unmarshal(raw, &p); err != nil || p.Service == "" {
		return nil, fmt.Errorf("service parameter required")
	}
	enabled := s.sup.ToggleWatch(p.Service)
	return map[string]bool{"watch_enabled": enabled}, nil
}

type tracesParams struct {
	Limit int `json:"limit"`
}

type traceSummary struct {
	TraceID   string `json:"trace_id"`
	RootStart string `json:"root_start"`
	SpanCount int    `json:"span_count"`
	RootName  string `json:"root_name,omitempty"`
	Services  []string `json:"services,omitempty"`
}

func (s *Server) handleTraces(raw json.RawMessage) (any, error) {
	store := s.getStore()
	if store == nil {
		return nil, fmt.Errorf("tracing is not enabled")
	}
	var p tracesParams
	_ = json.Unmarshal(raw, &p)
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}

	groups := store.Traces()
	if len(groups) > limit {
		groups = groups[:limit]
	}

	result := make([]traceSummary, len(groups))
	for i, g := range groups {
		ts := traceSummary{
			TraceID:   spanbuf.TraceIDHex(g.TraceID),
			RootStart: g.RootStart.Format(time.RFC3339Nano),
			SpanCount: len(g.Spans),
		}
		svcSet := make(map[string]bool)
		for _, sp := range g.Spans {
			svcSet[sp.ServiceName] = true
			if sp.ParentSpanID == [8]byte{} && ts.RootName == "" {
				ts.RootName = sp.Name
			}
		}
		for svc := range svcSet {
			ts.Services = append(ts.Services, svc)
		}
		result[i] = ts
	}
	return result, nil
}

type spansParams struct {
	TraceID string `json:"trace_id"`
	Service string `json:"service"`
}

type spanInfo struct {
	TraceID      string `json:"trace_id"`
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
	Name         string `json:"name"`
	ServiceName  string `json:"service_name"`
	Kind         string `json:"kind"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	Duration     string `json:"duration"`
	Status       string `json:"status"`
	StatusMsg    string `json:"status_msg,omitempty"`
}

func (s *Server) handleSpans(raw json.RawMessage) (any, error) {
	store := s.getStore()
	if store == nil {
		return nil, fmt.Errorf("tracing is not enabled")
	}
	var p spansParams
	_ = json.Unmarshal(raw, &p)

	var spans []spanbuf.Span

	if p.TraceID != "" {
		traceBytes, err := hex.DecodeString(p.TraceID)
		if err != nil || len(traceBytes) != 16 {
			return nil, fmt.Errorf("invalid trace_id")
		}
		var traceID [16]byte
		copy(traceID[:], traceBytes)
		spans = store.SpansByTrace(traceID)
	} else if p.Service != "" {
		spans = store.SpansByService(p.Service)
	} else {
		return nil, fmt.Errorf("trace_id or service parameter required")
	}

	result := make([]spanInfo, len(spans))
	for i, sp := range spans {
		si := spanInfo{
			TraceID:     spanbuf.TraceIDHex(sp.TraceID),
			SpanID:      spanbuf.SpanIDHex(sp.SpanID),
			Name:        sp.Name,
			ServiceName: sp.ServiceName,
			Kind:        sp.Kind.String(),
			StartTime:   sp.StartTime.Format(time.RFC3339Nano),
			EndTime:     sp.EndTime.Format(time.RFC3339Nano),
			Duration:    sp.Duration.String(),
			Status:      sp.Status.String(),
			StatusMsg:   sp.StatusMsg,
		}
		if sp.ParentSpanID != [8]byte{} {
			si.ParentSpanID = spanbuf.SpanIDHex(sp.ParentSpanID)
		}
		result[i] = si
	}
	return result, nil
}

type serviceMapEdge struct {
	From        string `json:"from"`
	To          string `json:"to"`
	CallCount   int    `json:"call_count"`
	ErrorCount  int    `json:"error_count"`
	AvgDuration string `json:"avg_duration"`
}

func (s *Server) handleServiceMap(_ json.RawMessage) (any, error) {
	store := s.getStore()
	if store == nil {
		return nil, fmt.Errorf("tracing is not enabled")
	}
	sm := store.ServiceMap()
	result := make([]serviceMapEdge, len(sm.Edges))
	for i, e := range sm.Edges {
		result[i] = serviceMapEdge{
			From:        e.From,
			To:          e.To,
			CallCount:   e.CallCount,
			ErrorCount:  e.ErrorCount,
			AvgDuration: e.AvgDuration.String(),
		}
	}
	return result, nil
}
