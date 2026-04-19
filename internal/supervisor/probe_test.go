package supervisor

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/logbuf"
)

// helper: build a ReadinessConfig with a given kind and useful defaults for tests.
func tcpReadiness(addr string, timeout, initialDelay time.Duration) config.ReadinessConfig {
	return config.ReadinessConfig{
		Kind:         "tcp",
		Address:      addr,
		Timeout:      config.Duration{Duration: timeout},
		InitialDelay: config.Duration{Duration: initialDelay},
	}
}

func httpReadiness(url string, timeout time.Duration) config.ReadinessConfig {
	return config.ReadinessConfig{
		Kind:    "http",
		URL:     url,
		Timeout: config.Duration{Duration: timeout},
	}
}

func logPatternReadiness(pattern string) config.ReadinessConfig {
	return config.ReadinessConfig{
		Kind:    "log_pattern",
		Pattern: pattern,
	}
}

// listenerAddr returns a 127.0.0.1:<port> address for a net.Listener.
func listenerAddr(l net.Listener) string { return l.Addr().String() }

// freeAddr allocates and immediately closes a tcp port, returning its address.
// Useful for "never-listening" tests. There is a theoretical race where the OS
// reassigns the port before the test uses it; in practice the risk is tiny.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestProbeTCP_Ready(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if !runProbe(ctx, tcpReadiness(listenerAddr(l), 200*time.Millisecond, 0), nil, 0) {
		t.Fatalf("expected TCP probe to succeed")
	}
}

func TestProbeTCP_RetriesThenReady(t *testing.T) {
	// Reserve a port and close it, then reopen after a short delay.
	addr := freeAddr(t)

	go func() {
		time.Sleep(80 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return // Test will fail on probe timeout below.
		}
		// Keep listener open until the test ends. Accept loop drains clients.
		go func() {
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
		t.Cleanup(func() { _ = l.Close() })
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	if !runProbe(ctx, tcpReadiness(addr, 200*time.Millisecond, 0), nil, 0) {
		t.Fatalf("expected TCP probe to succeed after retry")
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("probe returned suspiciously fast (%s) — retry loop may not have exercised", elapsed)
	}
}

func TestProbeTCP_CancelledBeforeReady(t *testing.T) {
	addr := freeAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		done <- runProbe(ctx, tcpReadiness(addr, 100*time.Millisecond, 0), nil, 0)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case result := <-done:
		if result {
			t.Fatalf("expected probe to return false on cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not return after cancel")
	}
}

func TestProbeHTTP_2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if !runProbe(ctx, httpReadiness(srv.URL, 500*time.Millisecond), nil, 0) {
		t.Fatalf("expected HTTP probe to succeed on 200")
	}
}

func TestProbeHTTP_5xxNeverReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if runProbe(ctx, httpReadiness(srv.URL, 200*time.Millisecond), nil, 0) {
		t.Fatalf("expected HTTP probe to return false (never reaches 2xx)")
	}
}

func TestProbeHTTP_NonDialable(t *testing.T) {
	addr := freeAddr(t)
	url := "http://" + addr

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if runProbe(ctx, httpReadiness(url, 100*time.Millisecond), nil, 0) {
		t.Fatalf("expected HTTP probe to return false against closed port")
	}
}

func TestProbeLogPattern_MatchAfterBaseline(t *testing.T) {
	buf := logbuf.New(100)
	buf.Add("stdout", "warming up")
	buf.Add("stdout", "loading config")
	last := buf.Last(1)
	if len(last) != 1 {
		t.Fatal("expected buffer to have lines")
	}
	baseline := last[0].Seq

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		done <- runProbe(ctx, logPatternReadiness("listening on"), buf, baseline)
	}()

	time.Sleep(50 * time.Millisecond)
	buf.Add("stdout", "server listening on :8080")

	select {
	case result := <-done:
		if !result {
			t.Fatalf("expected probe to succeed after matching line was added")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not return in time")
	}
}

func TestProbeLogPattern_IgnoresPreBaseline(t *testing.T) {
	buf := logbuf.New(100)
	buf.Add("stdout", "server listening on :8080") // matching line, but pre-baseline
	last := buf.Last(1)
	baseline := last[0].Seq

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		done <- runProbe(ctx, logPatternReadiness("listening on"), buf, baseline)
	}()

	select {
	case result := <-done:
		if result {
			t.Fatalf("probe matched a pre-baseline line — baseline filter is broken")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not return after ctx timeout")
	}
}

func TestProbeInitialDelay(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cfg := tcpReadiness(listenerAddr(l), 200*time.Millisecond, 200*time.Millisecond)

	start := time.Now()
	if !runProbe(ctx, cfg, nil, 0) {
		t.Fatalf("expected probe to eventually succeed")
	}
	if elapsed := time.Since(start); elapsed < 180*time.Millisecond {
		t.Errorf("probe returned after %s — InitialDelay was not honoured", elapsed)
	}
}

func TestProbeBadRegex(t *testing.T) {
	buf := logbuf.New(100)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	goroutinesBefore := runtime.NumGoroutine()
	cfg := logPatternReadiness("[invalid(regex") // unclosed character class

	result := runProbe(ctx, cfg, buf, 0)
	if result {
		t.Fatalf("expected probe to return false on bad regex")
	}

	// Verify the error was logged.
	var matched bool
	for _, line := range buf.Lines() {
		if strings.Contains(line.Text, "invalid log_pattern regex") {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("expected bad-regex error to be logged to the service log buffer")
	}

	// Allow a moment for any runaway goroutine to register.
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > goroutinesBefore+1 {
		t.Errorf("goroutine leak suspected: before=%d after=%d", goroutinesBefore, after)
	}
}

func TestRunProbe_NoneKindIsInstantReady(t *testing.T) {
	// A service with Kind="" or Kind="none" should never be started with a
	// probe goroutine in the supervisor, but runProbe itself should return
	// true immediately if called — guards against future refactors.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if !runProbe(ctx, config.ReadinessConfig{Kind: ""}, nil, 0) {
		t.Error("empty kind should be instant-ready")
	}
	if !runProbe(ctx, config.ReadinessConfig{Kind: "none"}, nil, 0) {
		t.Error("'none' kind should be instant-ready")
	}
}
