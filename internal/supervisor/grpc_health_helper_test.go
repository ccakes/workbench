//go:build !windows

package supervisor

import (
	"context"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// healthSvc is a minimal in-process Health server whose response status can
// be flipped at runtime. It implements only the unary Check; Watch (the
// streaming form) is not needed by our probe.
type healthSvc struct {
	healthpb.UnimplementedHealthServer
	mu     sync.Mutex
	status healthpb.HealthCheckResponse_ServingStatus
}

func (h *healthSvc) Check(_ context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return &healthpb.HealthCheckResponse{Status: h.status}, nil
}

// runningServers holds a process-wide registry of helpers by their listen
// address, so tests can flip a server's status without juggling references
// through closures.
var runningServers sync.Map // map[string]*healthSvc

// startGRPCHealthServer spins up an in-process gRPC server registered with
// the Health service, returns its listen address, and arranges teardown at
// the end of the test. Service name is currently ignored (the helper has
// one status for all services), which matches what the probe asks for.
func startGRPCHealthServer(t *testing.T, initial healthpb.HealthCheckResponse_ServingStatus, _ string) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()

	srv := grpc.NewServer()
	hs := &healthSvc{status: initial}
	healthpb.RegisterHealthServer(srv, hs)
	runningServers.Store(addr, hs)

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		runningServers.Delete(addr)
		srv.GracefulStop()
	})
	return addr
}

// grpcHealthFlip changes the status returned by the named server.
func grpcHealthFlip(addr string, status healthpb.HealthCheckResponse_ServingStatus) {
	v, ok := runningServers.Load(addr)
	if !ok {
		return
	}
	hs := v.(*healthSvc)
	hs.mu.Lock()
	hs.status = status
	hs.mu.Unlock()
}

// suppress unused-import lint when this file is compiled alone
var _ = insecure.NewCredentials
