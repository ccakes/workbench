package api

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ccakes/workbench/internal/events"
)

// Client communicates with a running bench instance over a Unix socket.
type Client struct {
	sockPath string
}

// NewClient creates a client for the given socket path.
func NewClient(sockPath string) *Client {
	return &Client{sockPath: sockPath}
}

// Call sends a request and returns the response data.
func (c *Client) Call(method string, params any) (json.RawMessage, error) {
	return c.CallWithTimeout(method, params, 10*time.Second)
}

// CallWithTimeout sends a request with a caller-supplied connection deadline.
// A timeout <= 0 leaves the connection without a deadline after the initial
// dial timeout, which is useful for long-running calls such as wait.
func (c *Client) CallWithTimeout(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	conn, err := net.DialTimeout("unix", c.sockPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connecting to bench: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	req := Request{Method: method, Params: nil}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshaling params: %w", err)
		}
		msg := json.RawMessage(raw)
		req.Params = &msg
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}
	data = append(data, '\n')

	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("reading response: %w", err)
		}
		return nil, fmt.Errorf("no response from server")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if !resp.OK {
		errMsg := "unknown error"
		if resp.Error != "" {
			errMsg = resp.Error
		}
		return nil, fmt.Errorf("%s", errMsg)
	}

	return resp.Data, nil
}

// Ping checks if the server is reachable.
func (c *Client) Ping() error {
	_, err := c.Call("ping", nil)
	return err
}

// Subscribe opens a streaming connection to the server's event bus. It returns a
// channel of events and a stop function. The channel is closed when the stream
// ends — server shutdown, disconnect, or stop() being called. An error is
// returned if the connection fails or another client already holds the stream
// (the single interactive-client rule).
func (c *Client) Subscribe() (<-chan events.Event, func(), error) {
	conn, err := net.DialTimeout("unix", c.sockPath, 5*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to bench: %w", err)
	}

	req := Request{Method: "subscribe"}
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("sending subscribe: %w", err)
	}

	r := bufio.NewReaderSize(conn, 1024*1024)

	// First line is a standard Response — confirms attach or reports that another
	// client already holds the stream.
	line, err := r.ReadBytes('\n')
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("reading subscribe response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("decoding subscribe response: %w", err)
	}
	if !resp.OK {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("%s", resp.Error)
	}

	out := make(chan events.Event, 256)
	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var w WireEvent
			if json.Unmarshal(line, &w) != nil {
				continue
			}
			select {
			case out <- w.ToEvent():
			case <-done:
				return
			}
		}
	}()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(done)
			_ = conn.Close()
		})
	}
	return out, stop, nil
}

// SocketPath derives the socket path from a config file's absolute path.
func SocketPath(configPath string) (string, error) {
	dir, err := privateSocketDir()
	if err != nil {
		return "", err
	}
	name, err := SocketName(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// SocketName returns the per-config socket filename. It depends only on the
// absolute config path, not on $TMPDIR, so the same config always yields the
// same filename regardless of which temp directory it lives in.
func SocketName(configPath string) (string, error) {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("resolving config path: %w", err)
	}
	h := sha256.Sum256([]byte(abs))
	return fmt.Sprintf("bench-%x.sock", h[:4]), nil
}

// candidateSocketDirs returns the per-user bench socket directories to search.
// The server places its socket under os.TempDir()/bench-<uid>, but os.TempDir()
// honors $TMPDIR — so a `bench up` in a login shell and a control command run
// from an agent (which often sets its own $TMPDIR) compute different
// directories. Searching the common temp roots lets discovery find the socket
// regardless of which environment started the server.
func candidateSocketDirs() []string {
	uid := os.Getuid()
	name := "bench-user"
	if uid >= 0 {
		name = fmt.Sprintf("bench-%d", uid)
	}

	var dirs []string
	seen := map[string]bool{}
	add := func(base string) {
		if base == "" {
			return
		}
		p := filepath.Join(base, name)
		if !seen[p] {
			seen[p] = true
			dirs = append(dirs, p)
		}
	}

	add(os.TempDir()) // honors $TMPDIR — current environment first
	add("/tmp")       // Go's os.TempDir() fallback when $TMPDIR is unset
	add("/var/tmp")
	// macOS per-user temp dirs: /var/folders/XX/YYYYYYYY/T
	if matches, err := filepath.Glob("/var/folders/*/*/T"); err == nil {
		for _, m := range matches {
			add(m)
		}
	}
	return dirs
}

// ExistingSockets returns the socket paths for a config that actually exist on
// disk, across all candidate temp directories. The default location (current
// $TMPDIR) is returned first when present. Stale files are included; callers
// should Ping to confirm a live server.
func ExistingSockets(configPath string) ([]string, error) {
	name, err := SocketName(configPath)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, dir := range candidateSocketDirs() {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSocket != 0 {
			out = append(out, p)
		}
	}
	return out, nil
}

// FindSocket locates the socket for a config, searching candidate temp
// directories. It returns the first live socket (one that responds to Ping).
// If none is live it falls back to the first existing socket file, and failing
// that to the default computed path. The bool reports whether a live server
// was found.
func FindSocket(configPath string) (string, bool, error) {
	existing, err := ExistingSockets(configPath)
	if err != nil {
		return "", false, err
	}
	for _, p := range existing {
		if NewClient(p).Ping() == nil {
			return p, true, nil
		}
	}
	if len(existing) > 0 {
		return existing[0], false, nil
	}
	def, err := SocketPath(configPath)
	if err != nil {
		return "", false, err
	}
	return def, false, nil
}

// SocketPathFromEnvOrConfig returns the socket path from BENCH_SOCKET env,
// a flag override, or derives it from the config path.
func SocketPathFromEnvOrConfig(socketOverride, configPath string) (string, error) {
	if socketOverride != "" {
		return socketOverride, nil
	}
	if envSock := os.Getenv("BENCH_SOCKET"); envSock != "" {
		return envSock, nil
	}
	return SocketPath(configPath)
}

func privateSocketDir() (string, error) {
	uid := os.Getuid()
	name := "bench-user"
	if uid >= 0 {
		name = fmt.Sprintf("bench-%d", uid)
	}
	return filepath.Join(os.TempDir(), name), nil
}

// EnsureSocketDir makes sure the private per-user socket directory that holds
// sockPath exists with hardened permissions (0700, not a symlink). It is a
// no-op when sockPath lives outside that directory (e.g. a custom --socket or
// BENCH_SOCKET path). Safe to call repeatedly; both the launcher (before
// writing the daemon log) and the daemon (before binding) invoke it.
func EnsureSocketDir(sockPath string) error {
	dir, err := privateSocketDir()
	if err != nil {
		return err
	}
	if filepath.Clean(filepath.Dir(sockPath)) != filepath.Clean(dir) {
		return nil
	}
	info, err := os.Lstat(dir)
	switch {
	case os.IsNotExist(err):
		if err := os.Mkdir(dir, 0o700); err != nil {
			return fmt.Errorf("creating socket directory %s: %w", dir, err)
		}
	case err != nil:
		return fmt.Errorf("checking socket directory %s: %w", dir, err)
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("socket directory %s is a symlink", dir)
	case !info.IsDir():
		return fmt.Errorf("socket directory %s is not a directory", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("protecting socket directory %s: %w", dir, err)
	}
	return nil
}
