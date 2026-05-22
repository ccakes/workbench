package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"time"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/logbuf"
)

const (
	probeDefaultTimeout    = 2 * time.Second
	probeRetryInterval     = 500 * time.Millisecond
	logPatternPollInterval = 200 * time.Millisecond
	logPatternBatchLimit   = 128
)

// runProbe blocks until the configured readiness check succeeds or ctx is
// cancelled. Returns true on success, false on cancellation, unrecoverable
// setup error (e.g. invalid regex), or exhausted MaxAttempts. baselineSeq is
// the log-buffer sequence number captured immediately before the probe
// started, so log_pattern scans only this process instance's output.
//
// When logs is non-nil, setup errors (bad regex, exec exit codes) are appended
// to it as a "probe" stream line so the user can diagnose without reading
// exit codes. On success, the configured `settle` delay is observed before
// returning true so dependents do not unblock during the gap between
// "probe passed" and "service really ready."
func runProbe(ctx context.Context, cfg config.ReadinessConfig, logs *logbuf.Buffer, baselineSeq uint64) bool {
	kind := cfg.Kind
	if kind == "" || kind == "none" {
		return true
	}

	perAttempt := cfg.Timeout.Duration
	if perAttempt <= 0 {
		perAttempt = probeDefaultTimeout
	}
	interval := cfg.Interval.Duration
	if interval <= 0 {
		interval = probeRetryInterval
	}

	if cfg.InitialDelay.Duration > 0 {
		if !sleepCtx(ctx, cfg.InitialDelay.Duration) {
			return false
		}
	}

	var ok bool
	switch kind {
	case "log_pattern":
		re, err := regexp.Compile(cfg.Pattern)
		if err != nil {
			if logs != nil {
				logs.Add("stderr", fmt.Sprintf("readiness: invalid log_pattern regex %q: %v", cfg.Pattern, err))
			}
			return false
		}
		ok = probeLogPattern(ctx, logs, re, baselineSeq)
	case "tcp":
		ok = retryProbe(ctx, cfg.MaxAttempts, interval, func() bool {
			return probeTCPOnce(ctx, cfg.Address, perAttempt)
		})
	case "http":
		ok = retryProbe(ctx, cfg.MaxAttempts, interval, func() bool {
			return probeHTTPOnce(ctx, cfg.URL, perAttempt)
		})
	case "exec":
		if cfg.Command == nil || len(cfg.Command.Parts) == 0 {
			if logs != nil {
				logs.Add("stderr", "readiness: exec kind requires a command")
			}
			return false
		}
		parts := cfg.Command.Parts
		ok = retryProbe(ctx, cfg.MaxAttempts, interval, func() bool {
			return probeExecOnce(ctx, parts, perAttempt, logs)
		})
	default:
		return false
	}

	if !ok {
		return false
	}
	if cfg.Settle.Duration > 0 {
		if !sleepCtx(ctx, cfg.Settle.Duration) {
			return false
		}
	}
	return true
}

// retryProbe repeatedly invokes attempt until it returns true, ctx is
// cancelled, or maxAttempts is reached (0 = unlimited). It sleeps `interval`
// between attempts.
func retryProbe(ctx context.Context, maxAttempts int, interval time.Duration, attempt func() bool) bool {
	tries := 0
	for {
		if attempt() {
			return true
		}
		tries++
		if ctx.Err() != nil {
			return false
		}
		if maxAttempts > 0 && tries >= maxAttempts {
			return false
		}
		if !sleepCtx(ctx, interval) {
			return false
		}
	}
}

// sleepCtx sleeps for d or returns false if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func probeLogPattern(ctx context.Context, logs *logbuf.Buffer, re *regexp.Regexp, baseline uint64) bool {
	if logs == nil {
		return false
	}
	cursor := baseline
	for {
		for _, line := range logs.LastAfter(cursor, logPatternBatchLimit) {
			if line.Seq > cursor {
				cursor = line.Seq
			}
			if re.MatchString(line.Text) {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(logPatternPollInterval):
		}
	}
}

func probeTCPOnce(ctx context.Context, addr string, perAttemptTimeout time.Duration) bool {
	var dialer net.Dialer
	attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
	defer cancel()
	conn, err := dialer.DialContext(attemptCtx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func probeHTTPOnce(ctx context.Context, url string, perAttemptTimeout time.Duration) bool {
	client := &http.Client{Timeout: perAttemptTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func probeExecOnce(ctx context.Context, parts []string, perAttemptTimeout time.Duration, logs *logbuf.Buffer) bool {
	attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
	defer cancel()
	cmd := exec.CommandContext(attemptCtx, parts[0], parts[1:]...)
	if logs != nil {
		outR, outW := io.Pipe()
		errR, errW := io.Pipe()
		cmd.Stdout = outW
		cmd.Stderr = errW
		go streamProbeLines(outR, logs, "probe")
		go streamProbeLines(errR, logs, "probe")
		defer outW.Close()
		defer errW.Close()
	}
	err := cmd.Run()
	return err == nil
}

func streamProbeLines(r io.Reader, logs *logbuf.Buffer, stream string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		logs.Add(stream, sc.Text())
	}
	_ = sc.Err() // ignore: pipe closure on cmd exit is normal
}
