package logbuf

import (
	"sync"
	"time"
)

type Line struct {
	Timestamp time.Time
	Stream    string // "stdout" or "stderr"
	Text      string
}

// Buffer is a thread-safe ring buffer for log lines.
type Buffer struct {
	mu    sync.RWMutex
	lines []Line
	max   int
	start int
	count int
}

func New(maxLines int) *Buffer {
	if maxLines <= 0 {
		maxLines = 5000
	}
	return &Buffer{
		lines: make([]Line, maxLines),
		max:   maxLines,
	}
}

func (b *Buffer) Add(stream, text string) {
	b.mu.Lock()
	idx := (b.start + b.count) % b.max
	b.lines[idx] = Line{
		Timestamp: time.Now(),
		Stream:    stream,
		Text:      text,
	}
	if b.count < b.max {
		b.count++
	} else {
		b.start = (b.start + 1) % b.max
	}
	b.mu.Unlock()
}

// Lines returns all buffered lines in order.
func (b *Buffer) Lines() []Line {
	b.mu.RLock()
	result := make([]Line, b.count)
	for i := range b.count {
		result[i] = b.lines[(b.start+i)%b.max]
	}
	b.mu.RUnlock()
	return result
}

// Last returns the last n lines.
func (b *Buffer) Last(n int) []Line {
	b.mu.RLock()
	if n > b.count {
		n = b.count
	}
	result := make([]Line, n)
	offset := b.count - n
	for i := range n {
		result[i] = b.lines[(b.start+offset+i)%b.max]
	}
	b.mu.RUnlock()
	return result
}

func (b *Buffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.count
}

func (b *Buffer) Clear() {
	b.mu.Lock()
	b.start = 0
	b.count = 0
	b.mu.Unlock()
}
