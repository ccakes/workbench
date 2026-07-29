package supervisor

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

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

	if !runProbe(ctx, tcpReadiness(listenerAddr(l), 200*time.Millisecond, 0), nil, 0, nil) {
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
	if !runProbe(ctx, tcpReadiness(addr, 200*time.Millisecond, 0), nil, 0, nil) {
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
		done <- runProbe(ctx, tcpReadiness(addr, 100*time.Millisecond, 0), nil, 0, nil)
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

	if !runProbe(ctx, httpReadiness(srv.URL, 500*time.Millisecond), nil, 0, nil) {
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

	if runProbe(ctx, httpReadiness(srv.URL, 200*time.Millisecond), nil, 0, nil) {
		t.Fatalf("expected HTTP probe to return false (never reaches 2xx)")
	}
}

func TestProbeHTTP_NonDialable(t *testing.T) {
	addr := freeAddr(t)
	url := "http://" + addr

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if runProbe(ctx, httpReadiness(url, 100*time.Millisecond), nil, 0, nil) {
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
		done <- runProbe(ctx, logPatternReadiness("listening on"), buf, baseline, nil)
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
		done <- runProbe(ctx, logPatternReadiness("listening on"), buf, baseline, nil)
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
	if !runProbe(ctx, cfg, nil, 0, nil) {
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

	result := runProbe(ctx, cfg, buf, 0, nil)
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

	if !runProbe(ctx, config.ReadinessConfig{Kind: ""}, nil, 0, nil) {
		t.Error("empty kind should be instant-ready")
	}
	if !runProbe(ctx, config.ReadinessConfig{Kind: "none"}, nil, 0, nil) {
		t.Error("'none' kind should be instant-ready")
	}
}

func TestProbeExec_ExitZeroReady(t *testing.T) {
	cfg := config.ReadinessConfig{
		Kind:    "exec",
		Command: &config.Command{Parts: []string{"sh", "-c", "exit 0"}},
		Timeout: config.Duration{Duration: 2 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !runProbe(ctx, cfg, nil, 0, nil) {
		t.Fatal("expected exec probe to succeed on exit 0")
	}
}

func TestProbeExec_StreamsOutputToLogs(t *testing.T) {
	buf := logbuf.New(50)
	cfg := config.ReadinessConfig{
		Kind:    "exec",
		Command: &config.Command{Parts: []string{"sh", "-c", "echo hello-probe && exit 0"}},
		Timeout: config.Duration{Duration: 2 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !runProbe(ctx, cfg, buf, 0, nil) {
		t.Fatal("expected exec probe to succeed")
	}
	// Give the streaming goroutine a moment to drain.
	time.Sleep(50 * time.Millisecond)
	found := false
	for _, line := range buf.Lines() {
		if strings.Contains(line.Text, "hello-probe") && line.Stream == "probe" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected exec probe stdout to appear in log buffer tagged 'probe', got: %v", buf.Lines())
	}
}

func TestProbeExec_MaxAttemptsCap(t *testing.T) {
	// A command that always fails should give up after MaxAttempts and return false.
	cfg := config.ReadinessConfig{
		Kind:        "exec",
		Command:     &config.Command{Parts: []string{"sh", "-c", "exit 1"}},
		Timeout:     config.Duration{Duration: 200 * time.Millisecond},
		Interval:    config.Duration{Duration: 10 * time.Millisecond},
		MaxAttempts: 3,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if runProbe(ctx, cfg, nil, 0, nil) {
		t.Fatal("expected exec probe to fail when command always exits non-zero")
	}
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Errorf("expected probe to give up quickly with MaxAttempts=3, took %v", elapsed)
	}
}

func TestProbeTCP_MaxAttemptsCap(t *testing.T) {
	// Closed port + MaxAttempts=2 should bail quickly instead of looping
	// until ctx cancellation.
	cfg := config.ReadinessConfig{
		Kind:        "tcp",
		Address:     freeAddr(t),
		Timeout:     config.Duration{Duration: 50 * time.Millisecond},
		Interval:    config.Duration{Duration: 10 * time.Millisecond},
		MaxAttempts: 2,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if runProbe(ctx, cfg, nil, 0, nil) {
		t.Fatal("expected tcp probe to give up after MaxAttempts")
	}
}

func TestProbeSettleDelaysReady(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()

	cfg := config.ReadinessConfig{
		Kind:    "tcp",
		Address: listenerAddr(l),
		Timeout: config.Duration{Duration: 200 * time.Millisecond},
		Settle:  config.Duration{Duration: 150 * time.Millisecond},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	if !runProbe(ctx, cfg, nil, 0, nil) {
		t.Fatal("expected probe to succeed")
	}
	elapsed := time.Since(start)
	if elapsed < 150*time.Millisecond {
		t.Errorf("expected runProbe to wait for settle, returned in %v", elapsed)
	}
}

func TestProbeGRPC_Serving(t *testing.T) {
	addr := startGRPCHealthServer(t, healthpb.HealthCheckResponse_SERVING, "")
	cfg := config.ReadinessConfig{
		Kind:    "grpc",
		Address: addr,
		Timeout: config.Duration{Duration: 500 * time.Millisecond},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !runProbe(ctx, cfg, nil, 0, nil) {
		t.Fatal("expected grpc probe to succeed when status=SERVING")
	}
}

func TestProbeGRPC_NotServingThenSucceeds(t *testing.T) {
	// Server starts NOT_SERVING, flips to SERVING after a delay. The probe
	// should retry through the not-serving period until it lands SERVING.
	addr := startGRPCHealthServer(t, healthpb.HealthCheckResponse_NOT_SERVING, "")
	go func() {
		time.Sleep(200 * time.Millisecond)
		grpcHealthFlip(addr, healthpb.HealthCheckResponse_SERVING)
	}()
	cfg := config.ReadinessConfig{
		Kind:     "grpc",
		Address:  addr,
		Timeout:  config.Duration{Duration: 200 * time.Millisecond},
		Interval: config.Duration{Duration: 50 * time.Millisecond},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !runProbe(ctx, cfg, nil, 0, nil) {
		t.Fatal("expected grpc probe to succeed after server flips to SERVING")
	}
}

func TestProbeGRPC_MaxAttemptsCap(t *testing.T) {
	addr := startGRPCHealthServer(t, healthpb.HealthCheckResponse_NOT_SERVING, "")
	cfg := config.ReadinessConfig{
		Kind:        "grpc",
		Address:     addr,
		Timeout:     config.Duration{Duration: 100 * time.Millisecond},
		Interval:    config.Duration{Duration: 10 * time.Millisecond},
		MaxAttempts: 2,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if runProbe(ctx, cfg, nil, 0, nil) {
		t.Fatal("expected grpc probe to give up after MaxAttempts when not SERVING")
	}
}

func TestProbeSettleCancellable(t *testing.T) {
	// If ctx cancels mid-settle, runProbe must return false rather than wait
	// out the settle delay.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()

	cfg := config.ReadinessConfig{
		Kind:    "tcp",
		Address: listenerAddr(l),
		Timeout: config.Duration{Duration: 200 * time.Millisecond},
		Settle:  config.Duration{Duration: 5 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if runProbe(ctx, cfg, nil, 0, nil) {
		t.Fatal("expected runProbe to return false when settle is interrupted")
	}
}

// stubExecer stands in for a ContainerRunner: it records the command the probe
// asked to run inside the container and returns a fixed host invocation.
type stubExecer struct {
	bin  string
	args []string
	got  []string
}

func (s *stubExecer) ExecCommand(cmd []string) (string, []string) {
	s.got = cmd
	return s.bin, s.args
}

func TestProbeContainerExec_ForwardsCommandAndSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ex := &stubExecer{bin: "sh", args: []string{"-c", "exit 0"}}
	cfg := config.ReadinessConfig{
		Kind:        "container_exec",
		Command:     &config.Command{Parts: []string{"pg_isready", "-U", "bench"}},
		Timeout:     config.Duration{Duration: 2 * time.Second},
		MaxAttempts: 1,
	}

	if !runProbe(ctx, cfg, nil, 0, ex) {
		t.Fatal("expected container_exec probe to succeed on exit 0")
	}
	// The configured command must reach the backend verbatim — the probe adds
	// the container and CLI, never rewrites what the user asked to run.
	want := []string{"pg_isready", "-U", "bench"}
	if !reflect.DeepEqual(ex.got, want) {
		t.Errorf("ExecCommand got %v, want %v", ex.got, want)
	}
}

func TestProbeContainerExec_NonZeroExitFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ex := &stubExecer{bin: "sh", args: []string{"-c", "exit 1"}}
	cfg := config.ReadinessConfig{
		Kind:        "container_exec",
		Command:     &config.Command{Parts: []string{"pg_isready"}},
		Timeout:     config.Duration{Duration: time.Second},
		Interval:    config.Duration{Duration: 10 * time.Millisecond},
		MaxAttempts: 2,
	}

	if runProbe(ctx, cfg, nil, 0, ex) {
		t.Fatal("expected container_exec probe to fail on non-zero exit")
	}
}

func TestProbeContainerExec_NilExecerFails(t *testing.T) {
	buf := logbuf.New(100)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	cfg := config.ReadinessConfig{
		Kind:        "container_exec",
		Command:     &config.Command{Parts: []string{"pg_isready"}},
		MaxAttempts: 1,
	}

	// A process service yields a nil execer; the probe must fail loudly rather
	// than hang or silently pass.
	if runProbe(ctx, cfg, buf, 0, nil) {
		t.Fatal("expected container_exec to fail for a non-container service")
	}
	if !logContains(buf, "only valid for a container service") {
		t.Error("expected the non-container error to be logged")
	}
}

func TestProbeContainerExec_MissingCommandFails(t *testing.T) {
	buf := logbuf.New(100)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ex := &stubExecer{bin: "true"}
	cfg := config.ReadinessConfig{Kind: "container_exec", MaxAttempts: 1}

	if runProbe(ctx, cfg, buf, 0, ex) {
		t.Fatal("expected container_exec to fail with no command")
	}
	if !logContains(buf, "requires a command") {
		t.Error("expected the missing-command error to be logged")
	}
}

func logContains(buf *logbuf.Buffer, substr string) bool {
	for _, line := range buf.Lines() {
		if strings.Contains(line.Text, substr) {
			return true
		}
	}
	return false
}
