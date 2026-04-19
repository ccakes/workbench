package supervisor

import (
	"context"
	"fmt"
	"net"
	"net/http"
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
// cancelled. Returns true on success, false on cancellation or unrecoverable
// setup error (e.g. invalid regex). baselineSeq is the log-buffer sequence
// number captured immediately before the probe started, so log_pattern scans
// only this process instance's output.
//
// When logs is non-nil, setup errors (bad regex) are appended to it as an
// "probe" stream line so the user can diagnose without reading exit codes.
func runProbe(ctx context.Context, cfg config.ReadinessConfig, logs *logbuf.Buffer, baselineSeq uint64) bool {
	kind := cfg.Kind
	if kind == "" || kind == "none" {
		return true
	}

	perAttempt := cfg.Timeout.Duration
	if perAttempt <= 0 {
		perAttempt = probeDefaultTimeout
	}

	if cfg.InitialDelay.Duration > 0 {
		if !sleepCtx(ctx, cfg.InitialDelay.Duration) {
			return false
		}
	}

	switch kind {
	case "log_pattern":
		re, err := regexp.Compile(cfg.Pattern)
		if err != nil {
			if logs != nil {
				logs.Add("stderr", fmt.Sprintf("readiness: invalid log_pattern regex %q: %v", cfg.Pattern, err))
			}
			return false
		}
		return probeLogPattern(ctx, logs, re, baselineSeq)
	case "tcp":
		return probeTCP(ctx, cfg.Address, perAttempt)
	case "http":
		return probeHTTP(ctx, cfg.URL, perAttempt)
	}
	return false
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

func probeTCP(ctx context.Context, addr string, perAttemptTimeout time.Duration) bool {
	var dialer net.Dialer
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		conn, err := dialer.DialContext(attemptCtx, "tcp", addr)
		cancel()
		if err == nil {
			_ = conn.Close()
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if !sleepCtx(ctx, probeRetryInterval) {
			return false
		}
	}
}

func probeHTTP(ctx context.Context, url string, perAttemptTimeout time.Duration) bool {
	client := &http.Client{Timeout: perAttemptTimeout}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			// Bad URL — not a retryable condition.
			return false
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return true
			}
		}
		if ctx.Err() != nil {
			return false
		}
		if !sleepCtx(ctx, probeRetryInterval) {
			return false
		}
	}
}
