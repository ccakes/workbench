package logbuf

import (
	"sync"
	"time"
)

type Line struct {
	Timestamp time.Time
	Stream    string // "stdout" or "stderr"
	Text      string
	Seq       uint64 // monotonic sequence number, unique per buffer
}

// Buffer is a thread-safe ring buffer for log lines.
type Buffer struct {
	mu    sync.RWMutex
	lines []Line
	max   int
	start int
	count int
	seq   uint64
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
	b.seq++
	idx := (b.start + b.count) % b.max
	b.lines[idx] = Line{
		Timestamp: time.Now(),
		Stream:    stream,
		Text:      text,
		Seq:       b.seq,
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

// LastAfter returns buffered lines with Seq > afterSeq, up to limit lines.
func (b *Buffer) LastAfter(afterSeq uint64, limit int) []Line {
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Scan backwards from the end to find matching lines
	var result []Line
	for i := b.count - 1; i >= 0 && len(result) < limit; i-- {
		line := b.lines[(b.start+i)%b.max]
		if line.Seq <= afterSeq {
			break // older lines are all below the cursor
		}
		result = append(result, line)
	}
	// Reverse to chronological order
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
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
