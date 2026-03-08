package spanbuf

import (
	"sort"
	"sync"
	"time"
)

// traceGroup holds all spans belonging to a single trace.
type traceGroup struct {
	spans     []Span
	totalBytes int
	rootStart  time.Time
	// edges tracks service map edges contributed by this trace for rollback on eviction.
	edges []serviceEdgeKey
}

type traceOrderEntry struct {
	traceID   [16]byte
	rootStart time.Time
}

// Store is a size-based ring buffer for spans with trace-level eviction.
type Store struct {
	mu        sync.RWMutex
	traces    map[[16]byte]*traceGroup
	order     []traceOrderEntry
	totalBytes int64
	maxBytes   int64
	svcMap    *serviceMap
}

// NewStore creates a span store with the given maximum byte capacity.
func NewStore(maxBytes int64) *Store {
	return &Store{
		traces:   make(map[[16]byte]*traceGroup),
		maxBytes: maxBytes,
		svcMap:   newServiceMap(),
	}
}

// Add inserts a span into the store. If the buffer exceeds maxBytes,
// the oldest complete trace is evicted.
func (s *Store) Add(span Span) {
	if span.ByteSize == 0 {
		span.ByteSize = EstimateSize(&span)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tg, exists := s.traces[span.TraceID]
	if !exists {
		tg = &traceGroup{
			rootStart: span.StartTime,
		}
		s.traces[span.TraceID] = tg
		s.order = append(s.order, traceOrderEntry{
			traceID:   span.TraceID,
			rootStart: span.StartTime,
		})
	}

	tg.spans = append(tg.spans, span)
	tg.totalBytes += span.ByteSize
	s.totalBytes += int64(span.ByteSize)

	// Update root start time if this span is earlier (likely the root)
	if span.StartTime.Before(tg.rootStart) {
		tg.rootStart = span.StartTime
		// Update order entry
		for i := range s.order {
			if s.order[i].traceID == span.TraceID {
				s.order[i].rootStart = span.StartTime
				break
			}
		}
	}

	// Update service map edges
	if span.ParentSpanID != [8]byte{} {
		parentSvc := s.findParentService(span.TraceID, span.ParentSpanID)
		if parentSvc != "" && parentSvc != span.ServiceName {
			ek := serviceEdgeKey{from: parentSvc, to: span.ServiceName}
			s.svcMap.addEdge(ek, span.Duration, span.Status == StatusError)
			tg.edges = append(tg.edges, ek)
		}
	}

	// Evict oldest traces while over budget
	for s.totalBytes > s.maxBytes && len(s.order) > 0 {
		s.evictOldest()
	}
}

// findParentService looks up the service name of a parent span within the same trace.
func (s *Store) findParentService(traceID [16]byte, parentSpanID [8]byte) string {
	tg, ok := s.traces[traceID]
	if !ok {
		return ""
	}
	for i := range tg.spans {
		if tg.spans[i].SpanID == parentSpanID {
			return tg.spans[i].ServiceName
		}
	}
	return ""
}

func (s *Store) evictOldest() {
	if len(s.order) == 0 {
		return
	}

	// Find the oldest entry
	oldestIdx := 0
	for i := 1; i < len(s.order); i++ {
		if s.order[i].rootStart.Before(s.order[oldestIdx].rootStart) {
			oldestIdx = i
		}
	}

	entry := s.order[oldestIdx]
	// Remove from order
	s.order = append(s.order[:oldestIdx], s.order[oldestIdx+1:]...)

	tg, ok := s.traces[entry.traceID]
	if !ok {
		return
	}

	// Roll back service map edges
	for _, ek := range tg.edges {
		s.svcMap.removeEdge(ek)
	}

	s.totalBytes -= int64(tg.totalBytes)
	delete(s.traces, entry.traceID)
}

// Len returns the total number of spans in the store.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, tg := range s.traces {
		n += len(tg.spans)
	}
	return n
}

// TraceCount returns the number of distinct traces.
func (s *Store) TraceCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.traces)
}

// BytesUsed returns the current buffer usage in bytes.
func (s *Store) BytesUsed() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalBytes
}

// MaxBytes returns the maximum buffer capacity.
func (s *Store) MaxBytes() int64 {
	return s.maxBytes
}

// Clear removes all spans from the store.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces = make(map[[16]byte]*traceGroup)
	s.order = nil
	s.totalBytes = 0
	s.svcMap = newServiceMap()
}

// TraceGroup is a read-only view of a trace's spans.
type TraceGroup struct {
	TraceID   [16]byte
	Spans     []Span
	RootStart time.Time
}

// Traces returns all trace groups, sorted by root start time (newest first).
func (s *Store) Traces() []TraceGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()

	groups := make([]TraceGroup, 0, len(s.traces))
	for id, tg := range s.traces {
		spans := make([]Span, len(tg.spans))
		copy(spans, tg.spans)
		groups = append(groups, TraceGroup{
			TraceID:   id,
			Spans:     spans,
			RootStart: tg.rootStart,
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].RootStart.After(groups[j].RootStart)
	})
	return groups
}

// Spans returns all spans, sorted by start time (newest first).
func (s *Store) Spans() []Span {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var all []Span
	for _, tg := range s.traces {
		all = append(all, tg.spans...)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartTime.After(all[j].StartTime)
	})
	return all
}

// SpansByService returns all spans from the given service, sorted by start time (newest first).
func (s *Store) SpansByService(name string) []Span {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Span
	for _, tg := range s.traces {
		for _, sp := range tg.spans {
			if sp.ServiceName == name {
				result = append(result, sp)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.After(result[j].StartTime)
	})
	return result
}

// SpansByTrace returns all spans belonging to the given trace ID, sorted by start time.
func (s *Store) SpansByTrace(traceID [16]byte) []Span {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tg, ok := s.traces[traceID]
	if !ok {
		return nil
	}
	result := make([]Span, len(tg.spans))
	copy(result, tg.spans)
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.Before(result[j].StartTime)
	})
	return result
}

// ServiceMap returns a snapshot of the service interaction graph.
func (s *Store) ServiceMap() ServiceMapSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.svcMap.snapshot()
}
