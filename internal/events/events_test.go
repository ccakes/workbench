package events

import (
	"testing"
	"time"
)

func TestPublishSubscribe(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(10)

	bus.Publish(Event{
		Type:    ServiceStateChanged,
		Service: "web",
		Data:    StateChangeData{OldStatus: "stopped", NewStatus: "running"},
	})

	select {
	case evt := <-ch:
		if evt.Type != ServiceStateChanged {
			t.Errorf("type: got %v, want %v", evt.Type, ServiceStateChanged)
		}
		if evt.Service != "web" {
			t.Errorf("service: got %q, want %q", evt.Service, "web")
		}
		data, ok := evt.Data.(StateChangeData)
		if !ok {
			t.Fatal("expected StateChangeData")
		}
		if data.NewStatus != "running" {
			t.Errorf("new status: got %q, want %q", data.NewStatus, "running")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestMultipleSubscribers(t *testing.T) {
	bus := NewBus()
	ch1 := bus.Subscribe(10)
	ch2 := bus.Subscribe(10)
	ch3 := bus.Subscribe(10)

	bus.Publish(Event{
		Type:    LogLine,
		Service: "api",
		Data:    LogLineData{Stream: StreamStdout, Line: "hello"},
	})

	for i, ch := range []chan Event{ch1, ch2, ch3} {
		select {
		case evt := <-ch:
			if evt.Type != LogLine {
				t.Errorf("subscriber %d: type got %v, want %v", i, evt.Type, LogLine)
			}
			if evt.Service != "api" {
				t.Errorf("subscriber %d: service got %q, want %q", i, evt.Service, "api")
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}
}

func TestUnsubscribe(t *testing.T) {
	bus := NewBus()
	ch1 := bus.Subscribe(10)
	ch2 := bus.Subscribe(10)

	bus.Unsubscribe(ch1)

	bus.Publish(Event{
		Type:    FileChanged,
		Service: "worker",
		Data:    FileChangeData{Path: "/app/main.go"},
	})

	// ch1 is closed, should yield zero value immediately
	evt, ok := <-ch1
	if ok {
		t.Errorf("expected ch1 to be closed, got event: %+v", evt)
	}

	// ch2 should still receive
	select {
	case evt := <-ch2:
		if evt.Type != FileChanged {
			t.Errorf("ch2: type got %v, want %v", evt.Type, FileChanged)
		}
	case <-time.After(time.Second):
		t.Fatal("ch2: timed out waiting for event")
	}
}

func TestSlowSubscriber(t *testing.T) {
	bus := NewBus()

	// Buffer size of 1 -- will fill immediately
	slow := bus.Subscribe(1)
	fast := bus.Subscribe(100)

	// Publish more events than the slow subscriber can buffer.
	// This must not block.
	for i := 0; i < 10; i++ {
		bus.Publish(Event{
			Type:    LogLine,
			Service: "db",
			Data:    LogLineData{Stream: StreamStderr, Line: "msg"},
		})
	}

	// The slow subscriber should have at most 1 event buffered
	received := 0
	for {
		select {
		case <-slow:
			received++
		default:
			goto doneSlow
		}
	}
doneSlow:
	if received > 1 {
		t.Errorf("slow subscriber got %d events, expected at most 1", received)
	}

	// Fast subscriber should have all 10
	fastCount := 0
	for {
		select {
		case <-fast:
			fastCount++
		default:
			goto doneFast
		}
	}
doneFast:
	if fastCount != 10 {
		t.Errorf("fast subscriber got %d events, expected 10", fastCount)
	}
}

func TestPublishTimestamp(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(10)

	before := time.Now()

	// Publish with zero timestamp -- should be filled in
	bus.Publish(Event{
		Type:    RestartScheduled,
		Service: "cron",
	})

	after := time.Now()

	select {
	case evt := <-ch:
		if evt.Timestamp.IsZero() {
			t.Fatal("expected non-zero timestamp")
		}
		if evt.Timestamp.Before(before) || evt.Timestamp.After(after) {
			t.Errorf("timestamp %v not in range [%v, %v]", evt.Timestamp, before, after)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Publish with explicit timestamp -- should be preserved
	explicit := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	bus.Publish(Event{
		Type:      LogLine,
		Service:   "cron",
		Timestamp: explicit,
	})

	select {
	case evt := <-ch:
		if !evt.Timestamp.Equal(explicit) {
			t.Errorf("expected explicit timestamp %v, got %v", explicit, evt.Timestamp)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}
