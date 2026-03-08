package spanbuf

import "time"

// SpanKind represents the type of span.
type SpanKind int

const (
	SpanKindUnspecified SpanKind = iota
	SpanKindInternal
	SpanKindServer
	SpanKindClient
	SpanKindProducer
	SpanKindConsumer
)

func (k SpanKind) String() string {
	switch k {
	case SpanKindInternal:
		return "internal"
	case SpanKindServer:
		return "server"
	case SpanKindClient:
		return "client"
	case SpanKindProducer:
		return "producer"
	case SpanKindConsumer:
		return "consumer"
	default:
		return "unspecified"
	}
}

// SpanStatus represents the status of a span.
type SpanStatus int

const (
	StatusUnset SpanStatus = iota
	StatusOK
	StatusError
)

func (s SpanStatus) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusError:
		return "error"
	default:
		return "unset"
	}
}

// Attribute is a key-value pair attached to a span.
type Attribute struct {
	Key   string
	Value string
}

// SpanEvent is a time-stamped annotation on a span.
type SpanEvent struct {
	Name       string
	Timestamp  time.Time
	Attributes []Attribute
}

// Span is the internal representation of an OTLP span.
type Span struct {
	TraceID      [16]byte
	SpanID       [8]byte
	ParentSpanID [8]byte
	Name         string
	ServiceName  string
	Kind         SpanKind
	StartTime    time.Time
	EndTime      time.Time
	Duration     time.Duration
	Status       SpanStatus
	StatusMsg    string
	Attributes   []Attribute
	Events       []SpanEvent
	ByteSize     int
}

// EstimateSize returns the estimated memory footprint of the span in bytes.
func EstimateSize(s *Span) int {
	// Fixed overhead: trace/span IDs, timestamps, enums, pointers
	size := 128

	size += len(s.Name)
	size += len(s.ServiceName)
	size += len(s.StatusMsg)

	for i := range s.Attributes {
		size += len(s.Attributes[i].Key) + len(s.Attributes[i].Value) + 16
	}
	for i := range s.Events {
		size += len(s.Events[i].Name) + 32
		for j := range s.Events[i].Attributes {
			size += len(s.Events[i].Attributes[j].Key) + len(s.Events[i].Attributes[j].Value) + 16
		}
	}
	return size
}

// TraceIDHex returns the trace ID as a hex string.
func TraceIDHex(id [16]byte) string {
	const hex = "0123456789abcdef"
	buf := make([]byte, 32)
	for i, b := range id {
		buf[i*2] = hex[b>>4]
		buf[i*2+1] = hex[b&0x0f]
	}
	return string(buf)
}

// SpanIDHex returns the span ID as a hex string.
func SpanIDHex(id [8]byte) string {
	const hex = "0123456789abcdef"
	buf := make([]byte, 16)
	for i, b := range id {
		buf[i*2] = hex[b>>4]
		buf[i*2+1] = hex[b&0x0f]
	}
	return string(buf)
}
