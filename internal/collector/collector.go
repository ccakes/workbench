package collector

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"
	colpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/ccakes/bench/internal/events"
	"github.com/ccakes/bench/internal/spanbuf"
)

// Collector is an OTLP HTTP trace collector.
type Collector struct {
	store  *spanbuf.Store
	bus    *events.Bus
	server *http.Server
	port   int
}

// New creates a new collector that writes spans to the given store.
func New(store *spanbuf.Store, bus *events.Bus, port int) *Collector {
	return &Collector{
		store: store,
		bus:   bus,
		port:  port,
	}
}

// Start begins listening for OTLP trace data. Non-blocking.
func (c *Collector) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", c.handleTraces)

	c.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", c.port),
		Handler: mux,
	}

	ln, err := net.Listen("tcp", c.server.Addr)
	if err != nil {
		return fmt.Errorf("collector listen on port %d: %w", c.port, err)
	}

	go func() { _ = c.server.Serve(ln) }()
	return nil
}

// Shutdown gracefully stops the collector.
func (c *Collector) Shutdown() error {
	if c.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.server.Shutdown(ctx)
}

// Port returns the configured port.
func (c *Collector) Port() int {
	return c.port
}

func (c *Collector) handleTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024)) // 16MB max
	defer func() { _ = r.Body.Close() }()
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	req := &colpb.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(body, req); err != nil {
		http.Error(w, "failed to decode protobuf", http.StatusBadRequest)
		return
	}

	count := 0
	for _, rs := range req.ResourceSpans {
		serviceName := extractServiceName(rs)
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				span := convertSpan(s, serviceName)
				c.store.Add(span)
				count++
			}
		}
	}

	if count > 0 && c.bus != nil {
		c.bus.Publish(events.Event{
			Type: events.SpanBatchReceived,
			Data: events.SpanBatchData{Count: count},
		})
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
}

func extractServiceName(rs *tracepb.ResourceSpans) string {
	if rs.Resource == nil {
		return "unknown"
	}
	for _, attr := range rs.Resource.Attributes {
		if attr.Key == "service.name" {
			if sv := attr.Value.GetStringValue(); sv != "" {
				return sv
			}
		}
	}
	return "unknown"
}

func convertSpan(s *tracepb.Span, serviceName string) spanbuf.Span {
	span := spanbuf.Span{
		Name:        s.Name,
		ServiceName: serviceName,
		Kind:        convertKind(s.Kind),
		StartTime:   time.Unix(0, int64(s.StartTimeUnixNano)),
		EndTime:     time.Unix(0, int64(s.EndTimeUnixNano)),
		Duration:    time.Duration(s.EndTimeUnixNano - s.StartTimeUnixNano),
	}

	copy(span.TraceID[:], s.TraceId)
	copy(span.SpanID[:], s.SpanId)
	copy(span.ParentSpanID[:], s.ParentSpanId)

	if s.Status != nil {
		switch s.Status.Code {
		case tracepb.Status_STATUS_CODE_OK:
			span.Status = spanbuf.StatusOK
		case tracepb.Status_STATUS_CODE_ERROR:
			span.Status = spanbuf.StatusError
		default:
			span.Status = spanbuf.StatusUnset
		}
		span.StatusMsg = s.Status.Message
	}

	span.Attributes = convertAttributes(s.Attributes)

	for _, e := range s.Events {
		span.Events = append(span.Events, spanbuf.SpanEvent{
			Name:       e.Name,
			Timestamp:  time.Unix(0, int64(e.TimeUnixNano)),
			Attributes: convertAttributes(e.Attributes),
		})
	}

	span.ByteSize = spanbuf.EstimateSize(&span)
	return span
}

func convertKind(k tracepb.Span_SpanKind) spanbuf.SpanKind {
	switch k {
	case tracepb.Span_SPAN_KIND_INTERNAL:
		return spanbuf.SpanKindInternal
	case tracepb.Span_SPAN_KIND_SERVER:
		return spanbuf.SpanKindServer
	case tracepb.Span_SPAN_KIND_CLIENT:
		return spanbuf.SpanKindClient
	case tracepb.Span_SPAN_KIND_PRODUCER:
		return spanbuf.SpanKindProducer
	case tracepb.Span_SPAN_KIND_CONSUMER:
		return spanbuf.SpanKindConsumer
	default:
		return spanbuf.SpanKindUnspecified
	}
}

func convertAttributes(attrs []*commonpb.KeyValue) []spanbuf.Attribute {
	if len(attrs) == 0 {
		return nil
	}
	result := make([]spanbuf.Attribute, len(attrs))
	for i, attr := range attrs {
		result[i] = spanbuf.Attribute{
			Key:   attr.Key,
			Value: anyValueToString(attr.Value),
		}
	}
	return result
}

func anyValueToString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch val := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return val.StringValue
	case *commonpb.AnyValue_IntValue:
		return fmt.Sprintf("%d", val.IntValue)
	case *commonpb.AnyValue_DoubleValue:
		return fmt.Sprintf("%g", val.DoubleValue)
	case *commonpb.AnyValue_BoolValue:
		if val.BoolValue {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v.Value)
	}
}
