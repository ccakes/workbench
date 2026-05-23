package api

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
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

// SocketPath derives the socket path from a config file's absolute path.
func SocketPath(configPath string) (string, error) {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("resolving config path: %w", err)
	}
	dir, err := privateSocketDir()
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(abs))
	return filepath.Join(dir, fmt.Sprintf("bench-%x.sock", h[:4])), nil
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

func ensurePrivateSocketDir(sockPath string) error {
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
