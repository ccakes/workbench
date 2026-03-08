package collector

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/protobuf/proto"
	colpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/ccakes/bench/internal/events"
	"github.com/ccakes/bench/internal/spanbuf"
)

func buildExportRequest(serviceName string, spanName string) *colpb.ExportTraceServiceRequest {
	return &colpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{
							Key:   "service.name",
							Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: serviceName}},
						},
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Spans: []*tracepb.Span{
							{
								TraceId:           []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
								SpanId:            []byte{1, 2, 3, 4, 5, 6, 7, 8},
								Name:              spanName,
								Kind:              tracepb.Span_SPAN_KIND_SERVER,
								StartTimeUnixNano: 1000000000,
								EndTimeUnixNano:   1100000000,
								Status: &tracepb.Status{
									Code:    tracepb.Status_STATUS_CODE_OK,
									Message: "success",
								},
								Attributes: []*commonpb.KeyValue{
									{
										Key:   "http.method",
										Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "GET"}},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func setupCollector(t *testing.T) (*Collector, *httptest.Server) {
	t.Helper()
	store := spanbuf.NewStore(1024 * 1024)
	bus := events.NewBus()
	c := New(store, bus, 0)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", c.handleTraces)
	server := httptest.NewServer(mux)

	return c, server
}

// ---------------------------------------------------------------------------
// POST valid protobuf
// ---------------------------------------------------------------------------

func TestCollector_PostValidProtobuf(t *testing.T) {
	store := spanbuf.NewStore(1024 * 1024)
	bus := events.NewBus()
	sub := bus.Subscribe(10)
	c := New(store, bus, 0)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", c.handleTraces)
	server := httptest.NewServer(mux)
	defer server.Close()

	req := buildExportRequest("my-service", "GET /api/users")
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal protobuf: %v", err)
	}

	resp, err := http.Post(server.URL+"/v1/traces", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Verify spans were stored
	if got := store.Len(); got != 1 {
		t.Errorf("store.Len() = %d, want 1", got)
	}

	spans := store.SpansByService("my-service")
	if len(spans) != 1 {
		t.Fatalf("SpansByService returned %d spans, want 1", len(spans))
	}
	if spans[0].Name != "GET /api/users" {
		t.Errorf("span name = %q, want %q", spans[0].Name, "GET /api/users")
	}
	if spans[0].Kind != spanbuf.SpanKindServer {
		t.Errorf("span kind = %v, want SpanKindServer", spans[0].Kind)
	}
	if spans[0].Status != spanbuf.StatusOK {
		t.Errorf("span status = %v, want StatusOK", spans[0].Status)
	}

	// Verify event was published
	select {
	case evt := <-sub:
		if evt.Type != events.SpanBatchReceived {
			t.Errorf("event type = %v, want SpanBatchReceived", evt.Type)
		}
		data, ok := evt.Data.(events.SpanBatchData)
		if !ok {
			t.Fatalf("event data type = %T, want SpanBatchData", evt.Data)
		}
		if data.Count != 1 {
			t.Errorf("SpanBatchData.Count = %d, want 1", data.Count)
		}
	default:
		t.Error("expected SpanBatchReceived event on bus")
	}
}

// ---------------------------------------------------------------------------
// POST invalid data returns 400
// ---------------------------------------------------------------------------

func TestCollector_PostInvalidData(t *testing.T) {
	_, server := setupCollector(t)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/traces", "application/x-protobuf", bytes.NewReader([]byte("not valid protobuf")))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------------------
// GET returns 405
// ---------------------------------------------------------------------------

func TestCollector_GetReturns405(t *testing.T) {
	_, server := setupCollector(t)
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/traces")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}
