package supervisor

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ccakes/bench/internal/config"
	"github.com/ccakes/bench/internal/events"
	"github.com/ccakes/bench/internal/logbuf"
	"github.com/ccakes/bench/internal/runner"
	"github.com/ccakes/bench/internal/service"
)

type Supervisor struct {
	cfg      *config.Config
	services map[string]*managedService
	bus      *events.Bus
	ctx      context.Context
	cancel   context.CancelFunc
}

type managedService struct {
	info *service.Info
	cfg  config.ServiceConfig
	key  string
	logs *logbuf.Buffer

	// run loop control — each channel is buffered(1)
	stopCh    chan struct{}
	restartCh chan string
	doneCh    chan struct{} // closed when run loop exits

	// runner state (only accessed from run loop goroutine)
	r runner.Runner

	running bool // whether the run loop is active
	mu      sync.Mutex
}

func New(cfg *config.Config, bus *events.Bus) *Supervisor {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Supervisor{
		cfg:      cfg,
		services: make(map[string]*managedService),
		bus:      bus,
		ctx:      ctx,
		cancel:   cancel,
	}

	for key, svcCfg := range cfg.Services {
		displayName := svcCfg.Name
		if displayName == "" {
			displayName = key
		}
		info := service.NewInfo(key, displayName)
		info.WatchEnabled = svcCfg.Watch.IsEnabled()
		if !svcCfg.GetAutoStart() {
			info.Status = service.StatusDisabled
		}
		if svcCfg.IsContainer() {
			info.ServiceType = "container"
			info.Image = svcCfg.Container.Image
			info.Ports = svcCfg.Container.Ports
		} else {
			info.ServiceType = "process"
		}

		s.services[key] = &managedService{
			info:      info,
			cfg:       svcCfg,
			key:       key,
			logs:      logbuf.New(cfg.Global.LogBufferLines),
			stopCh:    make(chan struct{}, 1),
			restartCh: make(chan string, 1),
			doneCh:    make(chan struct{}),
		}
	}
	return s
}

// Start launches all auto_start services in dependency order.
func (s *Supervisor) Start() error {
	order, err := s.cfg.StartOrder()
	if err != nil {
		return fmt.Errorf("resolving start order: %w", err)
	}

	for _, key := range order {
		ms := s.services[key]
		if !ms.cfg.GetAutoStart() {
			continue
		}
		s.launchRunLoop(ms)
	}
	return nil
}

func (s *Supervisor) launchRunLoop(ms *managedService) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.running {
		return
	}
	ms.running = true
	ms.stopCh = make(chan struct{}, 1)
	ms.restartCh = make(chan string, 1)
	ms.doneCh = make(chan struct{})
	go s.runLoop(ms)
}

// StartService starts a specific service by key.
func (s *Supervisor) StartService(key string) error {
	ms, ok := s.services[key]
	if !ok {
		return fmt.Errorf("unknown service %q", key)
	}
	ms.mu.Lock()
	if ms.running {
		ms.mu.Unlock()
		return nil
	}
	ms.mu.Unlock()
	s.launchRunLoop(ms)
	return nil
}

// StopService requests a graceful stop for a service.
func (s *Supervisor) StopService(key string) error {
	ms, ok := s.services[key]
	if !ok {
		return fmt.Errorf("unknown service %q", key)
	}
	ms.mu.Lock()
	if !ms.running {
		ms.mu.Unlock()
		return nil
	}
	ms.mu.Unlock()

	select {
	case ms.stopCh <- struct{}{}:
	default:
	}

	// Wait for run loop to exit
	<-ms.doneCh
	return nil
}

// RestartService restarts a running service, or starts it if stopped.
func (s *Supervisor) RestartService(key, reason string) error {
	ms, ok := s.services[key]
	if !ok {
		return fmt.Errorf("unknown service %q", key)
	}
	ms.mu.Lock()
	if !ms.running {
		ms.mu.Unlock()
		s.launchRunLoop(ms)
		return nil
	}
	ms.mu.Unlock()

	select {
	case ms.restartCh <- reason:
	default:
	}
	return nil
}

// Shutdown gracefully stops all services.
func (s *Supervisor) Shutdown() {
	s.cancel()

	var wg sync.WaitGroup
	for _, ms := range s.services {
		ms.mu.Lock()
		running := ms.running
		ms.mu.Unlock()
		if running {
			wg.Add(1)
			go func(ms *managedService) {
				defer wg.Done()
				select {
				case ms.stopCh <- struct{}{}:
				default:
				}
				<-ms.doneCh
			}(ms)
		}
	}
	wg.Wait()
}

// ServiceKeys returns service keys in start order.
func (s *Supervisor) ServiceKeys() []string {
	order, _ := s.cfg.StartOrder()
	return order
}

// ServiceInfo returns the info for a service.
func (s *Supervisor) ServiceInfo(key string) *service.Info {
	ms, ok := s.services[key]
	if !ok {
		return nil
	}
	return ms.info
}

// ServiceLogs returns the log buffer for a service.
func (s *Supervisor) ServiceLogs(key string) *logbuf.Buffer {
	ms, ok := s.services[key]
	if !ok {
		return nil
	}
	return ms.logs
}

// ServiceConfig returns the config for a service.
func (s *Supervisor) ServiceConfig(key string) *config.ServiceConfig {
	ms, ok := s.services[key]
	if !ok {
		return nil
	}
	return &ms.cfg
}

// ToggleWatch toggles file watching for a service.
func (s *Supervisor) ToggleWatch(key string) bool {
	ms, ok := s.services[key]
	if !ok {
		return false
	}
	ms.info.Lock()
	ms.info.WatchEnabled = !ms.info.WatchEnabled
	enabled := ms.info.WatchEnabled
	ms.info.Unlock()
	return enabled
}

// Bus returns the event bus.
func (s *Supervisor) Bus() *events.Bus {
	return s.bus
}

// Config returns the loaded configuration.
func (s *Supervisor) Config() *config.Config {
	return s.cfg
}

func (s *Supervisor) setStatus(ms *managedService, status service.Status, reason string) {
	ms.info.Lock()
	old := ms.info.Status
	ms.info.Status = status
	if reason != "" {
		ms.info.LastError = reason
	}
	ms.info.Unlock()

	s.bus.Publish(events.Event{
		Type:    events.ServiceStateChanged,
		Service: ms.key,
		Data: events.StateChangeData{
			OldStatus: old.String(),
			NewStatus: status.String(),
			Reason:    reason,
		},
	})
}

func (s *Supervisor) runLoop(ms *managedService) {
	defer func() {
		ms.mu.Lock()
		ms.running = false
		ms.mu.Unlock()
		close(ms.doneCh)
	}()

	retries := 0
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ms.stopCh:
			s.setStatus(ms, service.StatusStopped, "stopped")
			return
		default:
		}

		s.setStatus(ms, service.StatusStarting, "")

		// Create a fresh runner for each attempt
		if ms.cfg.IsContainer() {
			ms.r = runner.NewContainerRunner(ms.cfg, ms.key)
		} else {
			ms.r = runner.NewProcessRunner(ms.cfg)
		}

		env, err := s.buildEnv(ms)
		if err != nil {
			s.setStatus(ms, service.StatusFailed, err.Error())
			return
		}

		exitCh, err := ms.r.Start(env, ms.logs, s.bus, ms.key)
		if err != nil {
			ms.info.Lock()
			ms.info.LastError = err.Error()
			ms.info.Unlock()
			s.setStatus(ms, service.StatusFailed, err.Error())

			// On start failure, check restart policy
			if !s.shouldRestart(ms, 1) {
				return
			}
			retries++
			if ms.cfg.Restart.MaxRetries > 0 && retries > ms.cfg.Restart.MaxRetries {
				s.setStatus(ms, service.StatusFailed, fmt.Sprintf("max retries (%d) exceeded", ms.cfg.Restart.MaxRetries))
				return
			}
			s.setStatus(ms, service.StatusBackoff, "")
			if !s.waitBackoff(ms) {
				return
			}
			continue
		}

		// Update info from runner
		rInfo := ms.r.Info()
		ms.info.Lock()
		ms.info.PID = rInfo.PID
		ms.info.ContainerID = rInfo.ContainerID
		ms.info.StartTime = time.Now()
		ms.info.StopTime = time.Time{}
		ms.info.LastError = ""
		ms.info.Unlock()

		s.setStatus(ms, service.StatusRunning, "")

		// Wait for exit, stop, restart, or context cancel
		timeout := ms.cfg.GetShutdownTimeout(s.cfg.Global.ShutdownTimeout)

		select {
		case exitCode := <-exitCh:
			ms.info.Lock()
			ms.info.ExitCode = exitCode
			ms.info.PID = 0
			ms.info.ContainerID = ""
			ms.info.StopTime = time.Now()
			ms.info.Unlock()

			if !s.shouldRestart(ms, exitCode) {
				if exitCode == 0 {
					s.setStatus(ms, service.StatusStopped, "exited cleanly")
				} else {
					s.setStatus(ms, service.StatusFailed, fmt.Sprintf("exit code %d", exitCode))
				}
				return
			}

			retries++
			if ms.cfg.Restart.MaxRetries > 0 && retries > ms.cfg.Restart.MaxRetries {
				s.setStatus(ms, service.StatusFailed, fmt.Sprintf("max retries (%d) exceeded", ms.cfg.Restart.MaxRetries))
				return
			}

			ms.info.Lock()
			ms.info.RestartCount++
			ms.info.LastRestart = fmt.Sprintf("auto-restart (exit code %d)", exitCode)
			ms.info.Unlock()

			s.setStatus(ms, service.StatusBackoff, "")
			if !s.waitBackoff(ms) {
				return
			}

		case <-ms.stopCh:
			s.setStatus(ms, service.StatusStopping, "stopping")
			ms.r.Stop(exitCh, timeout)
			ms.info.Lock()
			ms.info.PID = 0
			ms.info.ContainerID = ""
			ms.info.StopTime = time.Now()
			ms.info.Unlock()
			s.setStatus(ms, service.StatusStopped, "stopped")
			return

		case reason := <-ms.restartCh:
			s.setStatus(ms, service.StatusRestarting, reason)
			ms.r.Stop(exitCh, timeout)
			ms.info.Lock()
			ms.info.PID = 0
			ms.info.ContainerID = ""
			ms.info.StopTime = time.Now()
			ms.info.RestartCount++
			ms.info.LastRestart = reason
			ms.info.Unlock()
			retries = 0

		case <-s.ctx.Done():
			s.setStatus(ms, service.StatusStopping, "shutting down")
			ms.r.Stop(exitCh, timeout)
			ms.info.Lock()
			ms.info.PID = 0
			ms.info.ContainerID = ""
			ms.info.StopTime = time.Now()
			ms.info.Unlock()
			s.setStatus(ms, service.StatusStopped, "shutdown")
			return
		}
	}
}

func (s *Supervisor) shouldRestart(ms *managedService, exitCode int) bool {
	switch ms.cfg.Restart.Policy {
	case "always":
		return true
	case "on-failure":
		return exitCode != 0
	default:
		return false
	}
}

func (s *Supervisor) waitBackoff(ms *managedService) bool {
	backoff := ms.cfg.Restart.Backoff.Duration
	if backoff <= 0 {
		backoff = time.Second
	}
	select {
	case <-time.After(backoff):
		return true
	case <-ms.stopCh:
		s.setStatus(ms, service.StatusStopped, "stopped during backoff")
		return false
	case reason := <-ms.restartCh:
		ms.info.Lock()
		ms.info.LastRestart = reason
		ms.info.Unlock()
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (s *Supervisor) buildEnv(ms *managedService) ([]string, error) {
	var env []string

	// For process services, inherit the full OS environment.
	// For containers, only pass config-specified env vars.
	if !ms.cfg.IsContainer() {
		env = os.Environ()
	}

	// Load global env file
	if s.cfg.Global.EnvFile != "" {
		fileEnv, err := config.LoadEnvFile(s.cfg.Global.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("global env_file: %w", err)
		}
		env = append(env, fileEnv...)
	}

	// Load service env file
	if ms.cfg.EnvFile != "" {
		fileEnv, err := config.LoadEnvFile(ms.cfg.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("env_file %q: %w", ms.cfg.EnvFile, err)
		}
		env = append(env, fileEnv...)
	}

	// OTEL env var injection when tracing is enabled
	if s.cfg.Global.Tracing.Enabled {
		port := s.cfg.Global.Tracing.Port
		hasEndpoint := false
		hasProtocol := false
		// Check service-level env, env file, and global env for existing OTEL vars
		for _, e := range env {
			if len(e) > 30 && e[:30] == "OTEL_EXPORTER_OTLP_ENDPOINT=" {
				hasEndpoint = true
			} else if len(e) > 29 && e[:29] == "OTEL_EXPORTER_OTLP_PROTOCOL=" {
				hasProtocol = true
			}
		}
		for k := range ms.cfg.Env {
			switch k {
			case "OTEL_EXPORTER_OTLP_ENDPOINT":
				hasEndpoint = true
			case "OTEL_EXPORTER_OTLP_PROTOCOL":
				hasProtocol = true
			}
		}
		if !hasEndpoint {
			env = append(env, fmt.Sprintf("OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:%d", port))
		}
		if !hasProtocol {
			env = append(env, "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf")
		}
	}

	// Inline env (highest priority)
	for k, v := range ms.cfg.Env {
		env = append(env, k+"="+v)
	}

	return env, nil
}
