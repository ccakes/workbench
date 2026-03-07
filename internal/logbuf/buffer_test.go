package logbuf

import (
	"fmt"
	"sync"
	"testing"
)

func TestAdd(t *testing.T) {
	buf := New(10)

	buf.Add("stdout", "hello")
	buf.Add("stderr", "world")

	lines := buf.Lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	if lines[0].Stream != "stdout" || lines[0].Text != "hello" {
		t.Errorf("line 0: got stream=%q text=%q, want stdout/hello", lines[0].Stream, lines[0].Text)
	}
	if lines[1].Stream != "stderr" || lines[1].Text != "world" {
		t.Errorf("line 1: got stream=%q text=%q, want stderr/world", lines[1].Stream, lines[1].Text)
	}

	// Timestamps should be set
	if lines[0].Timestamp.IsZero() {
		t.Error("expected non-zero timestamp on line 0")
	}
	if lines[1].Timestamp.IsZero() {
		t.Error("expected non-zero timestamp on line 1")
	}
}

func TestRingOverflow(t *testing.T) {
	cap := 5
	buf := New(cap)

	// Add more lines than capacity
	for i := 0; i < 8; i++ {
		buf.Add("stdout", fmt.Sprintf("line-%d", i))
	}

	lines := buf.Lines()
	if len(lines) != cap {
		t.Fatalf("expected %d lines, got %d", cap, len(lines))
	}

	// The oldest 3 lines (0, 1, 2) should have been dropped
	for i, line := range lines {
		expected := fmt.Sprintf("line-%d", i+3)
		if line.Text != expected {
			t.Errorf("lines[%d]: got %q, want %q", i, line.Text, expected)
		}
	}
}

func TestLast(t *testing.T) {
	buf := New(10)
	for i := 0; i < 7; i++ {
		buf.Add("stdout", fmt.Sprintf("line-%d", i))
	}

	// Request fewer than available
	last3 := buf.Last(3)
	if len(last3) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(last3))
	}
	for i, line := range last3 {
		expected := fmt.Sprintf("line-%d", i+4)
		if line.Text != expected {
			t.Errorf("last3[%d]: got %q, want %q", i, line.Text, expected)
		}
	}

	// Request more than available
	last20 := buf.Last(20)
	if len(last20) != 7 {
		t.Fatalf("expected 7 lines (clamped), got %d", len(last20))
	}

	// Request 0
	last0 := buf.Last(0)
	if len(last0) != 0 {
		t.Fatalf("expected 0 lines, got %d", len(last0))
	}
}

func TestClear(t *testing.T) {
	buf := New(10)
	buf.Add("stdout", "a")
	buf.Add("stdout", "b")

	if buf.Len() != 2 {
		t.Fatalf("expected 2 before clear, got %d", buf.Len())
	}

	buf.Clear()

	if buf.Len() != 0 {
		t.Errorf("expected 0 after clear, got %d", buf.Len())
	}
	lines := buf.Lines()
	if len(lines) != 0 {
		t.Errorf("expected empty lines after clear, got %d", len(lines))
	}
}

func TestConcurrent(t *testing.T) {
	t.Parallel()

	buf := New(100)
	var wg sync.WaitGroup

	// Writers
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				buf.Add("stdout", fmt.Sprintf("goroutine-%d-line-%d", id, i))
			}
		}(g)
	}

	// Readers
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = buf.Lines()
				_ = buf.Last(10)
				_ = buf.Len()
			}
		}()
	}

	wg.Wait()

	// After all writes the buffer should be full (1000 writes into capacity 100)
	if buf.Len() != 100 {
		t.Errorf("expected buffer full at 100, got %d", buf.Len())
	}
}

func TestLen(t *testing.T) {
	buf := New(5)

	if buf.Len() != 0 {
		t.Errorf("new buffer Len: got %d, want 0", buf.Len())
	}

	buf.Add("stdout", "a")
	if buf.Len() != 1 {
		t.Errorf("after 1 add: got %d, want 1", buf.Len())
	}

	buf.Add("stdout", "b")
	buf.Add("stdout", "c")
	if buf.Len() != 3 {
		t.Errorf("after 3 adds: got %d, want 3", buf.Len())
	}

	// Overflow: 7 adds into cap-5 buffer
	buf.Add("stdout", "d")
	buf.Add("stdout", "e")
	buf.Add("stdout", "f")
	buf.Add("stdout", "g")
	if buf.Len() != 5 {
		t.Errorf("after overflow: got %d, want 5", buf.Len())
	}
}
