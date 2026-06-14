package api

import (
	"encoding/json"
	"net"
	"time"

	"github.com/ccakes/workbench/internal/events"
)

// WireEvent is the JSON form of an events.Event sent over the subscribe stream.
// Data is flattened into optional fields so a single struct covers every event
// type without an interface on the wire.
type WireEvent struct {
	Type      string `json:"type"` // state | log | file | restart | span_batch
	Service   string `json:"service,omitempty"`
	TS        string `json:"ts"`
	OldStatus string `json:"old_status,omitempty"`
	NewStatus string `json:"new_status,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Stream    string `json:"stream,omitempty"`
	Line      string `json:"line,omitempty"`
	Path      string `json:"path,omitempty"`
	Count     int    `json:"count,omitempty"`
}

var wireTypeNames = map[events.Type]string{
	events.ServiceStateChanged: "state",
	events.LogLine:             "log",
	events.FileChanged:         "file",
	events.RestartScheduled:    "restart",
	events.SpanBatchReceived:   "span_batch",
}

var wireTypeValues = map[string]events.Type{
	"state":      events.ServiceStateChanged,
	"log":        events.LogLine,
	"file":       events.FileChanged,
	"restart":    events.RestartScheduled,
	"span_batch": events.SpanBatchReceived,
}

func toWireEvent(e events.Event) WireEvent {
	w := WireEvent{
		Type:    wireTypeNames[e.Type],
		Service: e.Service,
		TS:      e.Timestamp.Format(time.RFC3339Nano),
	}
	switch d := e.Data.(type) {
	case events.StateChangeData:
		w.OldStatus = d.OldStatus
		w.NewStatus = d.NewStatus
		w.Reason = d.Reason
	case events.LogLineData:
		w.Stream = d.Stream.String()
		w.Line = d.Line
	case events.FileChangeData:
		w.Path = d.Path
	case events.SpanBatchData:
		w.Count = d.Count
	}
	return w
}

// ToEvent reconstructs an events.Event from its wire form. Exported so the TUI
// client can feed the stream into the same event loop the in-process bus drives.
func (w WireEvent) ToEvent() events.Event {
	ts, err := time.Parse(time.RFC3339Nano, w.TS)
	if err != nil {
		ts = time.Time{}
	}
	e := events.Event{
		Type:      wireTypeValues[w.Type],
		Service:   w.Service,
		Timestamp: ts,
	}
	switch e.Type {
	case events.ServiceStateChanged:
		e.Data = events.StateChangeData{OldStatus: w.OldStatus, NewStatus: w.NewStatus, Reason: w.Reason}
	case events.LogLine:
		stream := events.StreamStdout
		if w.Stream == "stderr" {
			stream = events.StreamStderr
		}
		e.Data = events.LogLineData{Stream: stream, Line: w.Line}
	case events.FileChanged:
		e.Data = events.FileChangeData{Path: w.Path}
	case events.SpanBatchReceived:
		e.Data = events.SpanBatchData{Count: w.Count}
	}
	return e
}

// handleSubscribe streams bus events to a single interactive client. The first
// line written is a normal Response (ok/attached or an error if another client
// already holds the stream); subsequent lines are newline-delimited WireEvents.
func (s *Server) handleSubscribe(conn net.Conn) {
	if !s.attached.CompareAndSwap(false, true) {
		s.writeError(conn, "a TUI is already attached to this session")
		return
	}
	defer s.attached.Store(false)

	s.mu.RLock()
	sup := s.sup
	s.mu.RUnlock()
	if sup == nil {
		s.writeError(conn, "server is still starting up")
		return
	}

	// We now write continuously; drop the read deadline set by handleConn.
	_ = conn.SetReadDeadline(time.Time{})

	ch := sup.Bus().Subscribe(256)
	defer sup.Bus().Unsubscribe(ch)

	s.writeOK(conn, map[string]bool{"attached": true})

	// Detect client disconnect by reading from the connection; the client never
	// sends more data after the subscribe request, so any read result (EOF or
	// error) means the client is gone.
	clientGone := make(chan struct{})
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := conn.Read(buf); err != nil {
				close(clientGone)
				return
			}
		}
	}()

	for {
		select {
		case <-clientGone:
			return
		case <-s.closing:
			return
		case <-s.shutdownCh:
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			line, err := json.Marshal(toWireEvent(evt))
			if err != nil {
				continue
			}
			line = append(line, '\n')
			if _, err := conn.Write(line); err != nil {
				return
			}
		}
	}
}
