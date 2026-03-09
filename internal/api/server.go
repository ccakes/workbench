package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/ccakes/workbench/internal/spanbuf"
	"github.com/ccakes/workbench/internal/supervisor"
)

// Server listens on a Unix socket and handles control API requests.
type Server struct {
	mu       sync.RWMutex
	sup      *supervisor.Supervisor
	store    *spanbuf.Store // may be nil if tracing is disabled
	listener net.Listener
	sockPath string
	version  string
	handlers map[string]handlerFunc
	wg       sync.WaitGroup
}

type handlerFunc func(json.RawMessage) (any, error)

// New creates a new API server.
func New(sup *supervisor.Supervisor, store *spanbuf.Store, sockPath, version string) *Server {
	s := &Server{
		sup:      sup,
		store:    store,
		sockPath: sockPath,
		version:  version,
	}
	s.handlers = map[string]handlerFunc{
		"ping":         s.handlePing,
		"status":       s.handleStatus,
		"start":        s.handleStart,
		"stop":         s.handleStop,
		"restart":      s.handleRestart,
		"logs":         s.handleLogs,
		"toggle-watch": s.handleToggleWatch,
		"traces":       s.handleTraces,
		"spans":        s.handleSpans,
		"service-map":  s.handleServiceMap,
	}
	return s
}

// Start begins listening and accepting connections.
func (s *Server) Start() error {
	// Check for stale socket
	if err := cleanStaleSocket(s.sockPath); err != nil {
		return err
	}

	ln, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.sockPath, err)
	}
	s.listener = ln

	s.wg.Add(1)
	go s.acceptLoop()
	return nil
}

// Shutdown closes the listener, waits for in-flight connections, and removes the socket.
func (s *Server) Shutdown() {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.wg.Wait()
	_ = os.Remove(s.sockPath)
}

// SetSupervisor wires the supervisor into a running server.
// Called after the socket is bound but before services start.
func (s *Server) SetSupervisor(sup *supervisor.Supervisor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sup = sup
}

// SetStore wires the span store into a running server.
func (s *Server) SetStore(store *spanbuf.Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}

// SocketPath returns the socket path this server is listening on.
func (s *Server) SocketPath() string {
	return s.sockPath
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	if !scanner.Scan() {
		s.writeError(conn, "failed to read request")
		return
	}

	var req Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		s.writeError(conn, "invalid JSON: "+err.Error())
		return
	}

	handler, ok := s.handlers[req.Method]
	if !ok {
		s.writeError(conn, fmt.Sprintf("unknown method %q", req.Method))
		return
	}

	// Check supervisor is wired before dispatching
	s.mu.RLock()
	hasSup := s.sup != nil
	s.mu.RUnlock()
	if !hasSup && req.Method != "ping" {
		s.writeError(conn, "server is still starting up")
		return
	}

	var params json.RawMessage
	if req.Params != nil {
		params = *req.Params
	} else {
		params = json.RawMessage("{}")
	}

	data, err := handler(params)
	if err != nil {
		s.writeError(conn, err.Error())
		return
	}

	s.writeOK(conn, data)
}

func (s *Server) writeOK(conn net.Conn, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		s.writeError(conn, "marshal error: "+err.Error())
		return
	}
	resp := Response{OK: true, Data: raw}
	line, _ := json.Marshal(resp)
	line = append(line, '\n')
	_, _ = conn.Write(line)
}

func (s *Server) writeError(conn net.Conn, msg string) {
	resp := Response{OK: false, Error: msg}
	line, _ := json.Marshal(resp)
	line = append(line, '\n')
	_, _ = conn.Write(line)
}

// cleanStaleSocket checks if a socket file exists. If so, tries to connect.
// If connection is refused, it's stale and gets removed.
// If connection succeeds, another instance is running.
// Other errors (permissions, not a socket) are returned without removing the file.
func cleanStaleSocket(path string) error {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking socket %s: %w", path, err)
	}

	// Only attempt cleanup on socket files
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("socket path %s exists but is not a socket", path)
	}

	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		// Connection refused means no one is listening — stale socket
		if isConnectionRefused(err) {
			_ = os.Remove(path)
			return nil
		}
		// Other errors (permission denied, etc.) — don't blindly remove
		return fmt.Errorf("checking socket %s: %w", path, err)
	}
	_ = conn.Close()
	return fmt.Errorf("another bench instance is already running (socket: %s)", path)
}

// isConnectionRefused checks if an error is a "connection refused" error.
func isConnectionRefused(err error) bool {
	if opErr, ok := err.(*net.OpError); ok {
		if sysErr, ok := opErr.Err.(*os.SyscallError); ok {
			return sysErr.Err.Error() == "connection refused"
		}
	}
	return false
}
