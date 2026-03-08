package spanbuf

import (
	"sync"
	"testing"
	"time"
)

func makeSpan(traceID byte, spanID byte, service, name string, start time.Time) Span {
	s := Span{
		Name:        name,
		ServiceName: service,
		Kind:        SpanKindServer,
		StartTime:   start,
		EndTime:     start.Add(100 * time.Millisecond),
		Duration:    100 * time.Millisecond,
		Status:      StatusOK,
	}
	s.TraceID[0] = traceID
	s.SpanID[0] = spanID
	s.ByteSize = EstimateSize(&s)
	return s
}

func makeChildSpan(traceID byte, spanID byte, parentSpanID byte, service, name string, start time.Time) Span {
	s := makeSpan(traceID, spanID, service, name, start)
	s.ParentSpanID[0] = parentSpanID
	return s
}

// ---------------------------------------------------------------------------
// Add, Len, BytesUsed
// ---------------------------------------------------------------------------

func TestStore_AddAndLen(t *testing.T) {
	store := NewStore(1024 * 1024) // 1MB
	now := time.Now()

	store.Add(makeSpan(1, 1, "svc-a", "op1", now))
	store.Add(makeSpan(1, 2, "svc-a", "op2", now.Add(time.Millisecond)))
	store.Add(makeSpan(2, 3, "svc-b", "op3", now.Add(2*time.Millisecond)))

	if got := store.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}
	if got := store.TraceCount(); got != 2 {
		t.Errorf("TraceCount() = %d, want 2", got)
	}
	if got := store.BytesUsed(); got <= 0 {
		t.Errorf("BytesUsed() = %d, want > 0", got)
	}
}

// ---------------------------------------------------------------------------
// Eviction past size limit
// ---------------------------------------------------------------------------

func TestStore_EvictionRemovesOldestTrace(t *testing.T) {
	// Use a very small buffer so eviction triggers quickly.
	// Each span is ~140 bytes; set limit to hold roughly 2 spans.
	singleSize := EstimateSize(&Span{Name: "op", ServiceName: "svc"})
	maxBytes := int64(singleSize*2 + singleSize/2) // ~2.5 spans worth

	store := NewStore(maxBytes)
	now := time.Now()

	// Add two spans in trace 1 (oldest)
	store.Add(makeSpan(1, 1, "svc", "op1", now))

	// Add one span in trace 2 (newer)
	store.Add(makeSpan(2, 2, "svc", "op2", now.Add(time.Second)))

	// At this point, both traces should exist
	if store.TraceCount() != 2 {
		t.Fatalf("expected 2 traces before eviction, got %d", store.TraceCount())
	}

	// Add another span to trace 3 (newest) - should trigger eviction of trace 1
	store.Add(makeSpan(3, 3, "svc", "op3", now.Add(2*time.Second)))

	// Trace 1 (oldest) should have been evicted
	var traceID1 [16]byte
	traceID1[0] = 1
	if spans := store.SpansByTrace(traceID1); len(spans) != 0 {
		t.Errorf("expected trace 1 to be evicted, but got %d spans", len(spans))
	}

	if store.BytesUsed() > maxBytes {
		t.Errorf("BytesUsed() = %d, exceeds maxBytes = %d", store.BytesUsed(), maxBytes)
	}
}

// ---------------------------------------------------------------------------
// Concurrent read/write safety
// ---------------------------------------------------------------------------

func TestStore_ConcurrentReadWrite(t *testing.T) {
	store := NewStore(1024 * 1024)
	now := time.Now()
	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s := makeSpan(byte(id), byte(j), "svc", "op", now.Add(time.Duration(j)*time.Millisecond))
				store.Add(s)
			}
		}(i)
	}

	// Readers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = store.Len()
				_ = store.BytesUsed()
				_ = store.Spans()
				_ = store.Traces()
				_ = store.ServiceMap()
			}
		}()
	}

	wg.Wait()

	if store.Len() == 0 {
		t.Error("expected spans after concurrent writes")
	}
}

// ---------------------------------------------------------------------------
// SpansByService
// ---------------------------------------------------------------------------

func TestStore_SpansByService(t *testing.T) {
	store := NewStore(1024 * 1024)
	now := time.Now()

	store.Add(makeSpan(1, 1, "api", "handle", now))
	store.Add(makeSpan(1, 2, "db", "query", now.Add(time.Millisecond)))
	store.Add(makeSpan(2, 3, "api", "handle2", now.Add(2*time.Millisecond)))
	store.Add(makeSpan(2, 4, "cache", "get", now.Add(3*time.Millisecond)))

	apiSpans := store.SpansByService("api")
	if len(apiSpans) != 2 {
		t.Fatalf("SpansByService(\"api\") returned %d spans, want 2", len(apiSpans))
	}
	for _, s := range apiSpans {
		if s.ServiceName != "api" {
			t.Errorf("expected ServiceName = \"api\", got %q", s.ServiceName)
		}
	}

	// Newest first
	if !apiSpans[0].StartTime.After(apiSpans[1].StartTime) {
		t.Error("SpansByService should return spans sorted newest first")
	}

	dbSpans := store.SpansByService("db")
	if len(dbSpans) != 1 {
		t.Errorf("SpansByService(\"db\") returned %d spans, want 1", len(dbSpans))
	}

	missing := store.SpansByService("nonexistent")
	if len(missing) != 0 {
		t.Errorf("SpansByService(\"nonexistent\") returned %d spans, want 0", len(missing))
	}
}

// ---------------------------------------------------------------------------
// SpansByTrace
// ---------------------------------------------------------------------------

func TestStore_SpansByTrace(t *testing.T) {
	store := NewStore(1024 * 1024)
	now := time.Now()

	store.Add(makeSpan(1, 1, "svc", "op1", now))
	store.Add(makeSpan(1, 2, "svc", "op2", now.Add(time.Millisecond)))
	store.Add(makeSpan(2, 3, "svc", "op3", now.Add(2*time.Millisecond)))

	var traceID1 [16]byte
	traceID1[0] = 1
	spans := store.SpansByTrace(traceID1)
	if len(spans) != 2 {
		t.Fatalf("SpansByTrace returned %d spans, want 2", len(spans))
	}
	// Should be sorted by start time (oldest first)
	if !spans[0].StartTime.Before(spans[1].StartTime) {
		t.Error("SpansByTrace should return spans sorted oldest first")
	}

	var unknown [16]byte
	unknown[0] = 99
	if got := store.SpansByTrace(unknown); got != nil {
		t.Errorf("SpansByTrace for unknown trace returned %d spans, want nil", len(got))
	}
}

// ---------------------------------------------------------------------------
// Clear
// ---------------------------------------------------------------------------

func TestStore_Clear(t *testing.T) {
	store := NewStore(1024 * 1024)
	now := time.Now()

	store.Add(makeSpan(1, 1, "svc", "op1", now))
	store.Add(makeSpan(2, 2, "svc", "op2", now.Add(time.Millisecond)))

	if store.Len() == 0 {
		t.Fatal("expected spans before clear")
	}

	store.Clear()

	if got := store.Len(); got != 0 {
		t.Errorf("Len() after Clear() = %d, want 0", got)
	}
	if got := store.BytesUsed(); got != 0 {
		t.Errorf("BytesUsed() after Clear() = %d, want 0", got)
	}
	if got := store.TraceCount(); got != 0 {
		t.Errorf("TraceCount() after Clear() = %d, want 0", got)
	}
	snap := store.ServiceMap()
	if len(snap.Edges) != 0 {
		t.Errorf("ServiceMap edges after Clear() = %d, want 0", len(snap.Edges))
	}
}

// ---------------------------------------------------------------------------
// Service map tracks edges
// ---------------------------------------------------------------------------

func TestStore_ServiceMapEdges(t *testing.T) {
	store := NewStore(1024 * 1024)
	now := time.Now()

	// Create a parent span in "gateway" service
	parent := makeSpan(1, 1, "gateway", "handle", now)
	store.Add(parent)

	// Create a child span in "api" service that references the parent
	child := makeChildSpan(1, 2, 1, "api", "process", now.Add(time.Millisecond))
	store.Add(child)

	snap := store.ServiceMap()
	if len(snap.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(snap.Edges))
	}

	edge := snap.Edges[0]
	if edge.From != "gateway" || edge.To != "api" {
		t.Errorf("edge = %s -> %s, want gateway -> api", edge.From, edge.To)
	}
	if edge.CallCount != 1 {
		t.Errorf("CallCount = %d, want 1", edge.CallCount)
	}

	services := snap.Services()
	if len(services) != 2 {
		t.Errorf("Services() returned %d services, want 2", len(services))
	}
}

// ---------------------------------------------------------------------------
// Service map edge rollback on eviction
// ---------------------------------------------------------------------------

func TestStore_ServiceMapEdgeRollbackOnEviction(t *testing.T) {
	// Small buffer: fits roughly one span
	singleSize := EstimateSize(&Span{Name: "op", ServiceName: "svc"})
	maxBytes := int64(singleSize*3 + singleSize/2) // fits ~3.5 spans

	store := NewStore(maxBytes)
	now := time.Now()

	// Trace 1: gateway -> api edge (2 spans)
	parent1 := makeSpan(1, 1, "gateway", "handle", now)
	store.Add(parent1)
	child1 := makeChildSpan(1, 2, 1, "api", "process", now.Add(time.Millisecond))
	store.Add(child1)

	// Verify edge exists
	snap := store.ServiceMap()
	if len(snap.Edges) != 1 {
		t.Fatalf("expected 1 edge before eviction, got %d", len(snap.Edges))
	}

	// Trace 2: different services, enough spans to trigger eviction of trace 1
	parent2 := makeSpan(2, 3, "web", "request", now.Add(time.Second))
	store.Add(parent2)
	child2 := makeChildSpan(2, 4, 3, "cache", "lookup", now.Add(time.Second+time.Millisecond))
	store.Add(child2)

	// Trace 1 should be evicted and gateway->api edge should be rolled back
	var traceID1 [16]byte
	traceID1[0] = 1
	if spans := store.SpansByTrace(traceID1); len(spans) != 0 {
		t.Errorf("expected trace 1 to be evicted, but got %d spans", len(spans))
	}

	snap = store.ServiceMap()
	// gateway->api edge should be gone
	for _, e := range snap.Edges {
		if e.From == "gateway" && e.To == "api" {
			t.Error("expected gateway->api edge to be rolled back after eviction")
		}
	}
}
