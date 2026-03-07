package events

import (
	"sync"
	"time"
)

type Type int

const (
	ServiceStateChanged Type = iota
	LogLine
	FileChanged
	RestartScheduled
)

type Stream int

const (
	StreamStdout Stream = iota
	StreamStderr
)

func (s Stream) String() string {
	if s == StreamStdout {
		return "stdout"
	}
	return "stderr"
}

type Event struct {
	Type      Type
	Service   string
	Timestamp time.Time
	Data      any
}

type StateChangeData struct {
	OldStatus string
	NewStatus string
	Reason    string
}

type LogLineData struct {
	Stream Stream
	Line   string
}

type FileChangeData struct {
	Path string
}

// Bus is a simple pub/sub event bus. Subscribers receive events on buffered
// channels. Slow subscribers have events dropped rather than blocking publishers.
type Bus struct {
	mu   sync.RWMutex
	subs []chan Event
}

func NewBus() *Bus {
	return &Bus{}
}

func (b *Bus) Subscribe(bufSize int) chan Event {
	ch := make(chan Event, bufSize)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

func (b *Bus) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.subs {
		if sub == ch {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

func (b *Bus) Publish(evt Event) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}
