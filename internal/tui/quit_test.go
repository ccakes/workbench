package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/logbuf"
	"github.com/ccakes/workbench/internal/service"
	"github.com/ccakes/workbench/internal/spanbuf"
)

// fakeSession is a no-op Session for exercising model logic that doesn't need a
// real supervisor. canBackground toggles the background quit option.
type fakeSession struct {
	canBackground bool
	downCalled    bool
	ch            chan events.Event
}

func (f *fakeSession) Keys() []string                         { return nil }
func (f *fakeSession) Info(string) (service.Snapshot, bool)   { return service.Snapshot{}, false }
func (f *fakeSession) Config(string) *config.ServiceConfig    { return nil }
func (f *fakeSession) Logs(string) *logbuf.Buffer             { return nil }
func (f *fakeSession) ClearLogs(string)                       {}
func (f *fakeSession) StartService(string) error              { return nil }
func (f *fakeSession) StopService(string) error               { return nil }
func (f *fakeSession) RestartService(string, string) error    { return nil }
func (f *fakeSession) ToggleWatch(string) bool                { return false }
func (f *fakeSession) HasTracing() bool                       { return false }
func (f *fakeSession) Spans() []spanbuf.Span                  { return nil }
func (f *fakeSession) SpansByTrace([16]byte) []spanbuf.Span   { return nil }
func (f *fakeSession) ServiceMap() spanbuf.ServiceMapSnapshot { return spanbuf.ServiceMapSnapshot{} }
func (f *fakeSession) TraceStats() (int, int64)               { return 0, 0 }
func (f *fakeSession) Events() <-chan events.Event            { return f.ch }
func (f *fakeSession) Down() error                            { f.downCalled = true; return nil }
func (f *fakeSession) CanBackground() bool                    { return f.canBackground }
func (f *fakeSession) Close()                                 {}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestQuitDialogStopAndQuit(t *testing.T) {
	fs := &fakeSession{ch: make(chan events.Event)}
	m := NewModel(fs)
	m.confirmQuit = true

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if !isQuit(cmd) {
		t.Fatal("pressing 's' should quit")
	}
	if !fs.downCalled {
		t.Error("stop & quit should call Down()")
	}
	_ = next
}

func TestQuitDialogBackgroundAvailable(t *testing.T) {
	fs := &fakeSession{canBackground: true, ch: make(chan events.Event)}
	m := NewModel(fs)
	m.confirmQuit = true

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if !isQuit(cmd) {
		t.Fatal("pressing 'b' should quit (detach) when backgroundable")
	}
	if fs.downCalled {
		t.Error("background must NOT stop services")
	}
}

func TestQuitDialogBackgroundUnavailableCancels(t *testing.T) {
	fs := &fakeSession{canBackground: false, ch: make(chan events.Event)}
	m := NewModel(fs)
	m.confirmQuit = true

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if isQuit(cmd) {
		t.Fatal("'b' should not quit when background is unavailable")
	}
	if got := next.(Model); got.confirmQuit {
		t.Error("'b' with no background should cancel the dialog")
	}
}

func TestQuitDialogCancel(t *testing.T) {
	fs := &fakeSession{ch: make(chan events.Event)}
	m := NewModel(fs)
	m.confirmQuit = true

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if isQuit(cmd) {
		t.Fatal("esc should cancel, not quit")
	}
	if got := next.(Model); got.confirmQuit {
		t.Error("esc should dismiss the dialog")
	}
	if fs.downCalled {
		t.Error("cancel must not stop services")
	}
}
