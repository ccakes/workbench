package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/logbuf"
	"github.com/ccakes/workbench/internal/runner"
	"github.com/ccakes/workbench/internal/service"
)

// dependencyPollInterval is how often the run loop re-checks dependency
// status while waiting for deps to reach Running.
const dependencyPollInterval = 100 * time.Millisecond

type Supervisor struct {
	cfg      *config.Config
	services map[string]*managedService
	bus      *events.Bus
	ctx      context.Context
	cancel   context.CancelFunc
	backend  runner.ContainerBackend
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

	// startupErr is set by the probe/setup goroutine when readiness or setup
	// fails. The run loop's stop path checks this to surface Failed instead of
	// the usual Stopped, since the service was killed by its own bootstrap
	// failure rather than an operator stop.
	startupErr string
}

func New(cfg *config.Config, bus *events.Bus) *Supervisor {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Supervisor{
		cfg:      cfg,
		services: make(map[string]*managedService),
		bus:      bus,
		ctx:      ctx,
		cancel:   cancel,
		backend:  runner.ResolveBackend(cfg.Global),
	}

	for key, svcCfg := range cfg.Services {
		info := service.NewInfo(key, displayName(key, svcCfg))
		applyServiceMetadata(info, key, svcCfg, false, s.backend.Name())

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

func displayName(key string, svcCfg config.ServiceConfig) string {
	if svcCfg.Name != "" {
		return svcCfg.Name
	}
	return key
}

func applyServiceMetadata(info *service.Info, key string, svcCfg config.ServiceConfig, preserveStatus bool, backendName string) {
	info.Lock()
	defer info.Unlock()

	info.DisplayName = displayName(key, svcCfg)
	info.WatchEnabled = svcCfg.Watch.IsEnabled()
	if svcCfg.IsContainer() {
		info.ServiceType = "container"
		info.Backend = backendName
		info.Image = svcCfg.Container.Image
		info.Ports = append([]string(nil), svcCfg.Container.Ports...)
	} else {
		info.ServiceType = "process"
		info.Backend = ""
		info.Image = ""
		info.Ports = nil
	}
	if !preserveStatus && !svcCfg.GetAutoStart() {
		info.Status = service.StatusDisabled
	}
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

		if !s.waitForDependencies(ms) {
			return
		}

		s.setStatus(ms, service.StatusStarting, "")
		ms.mu.Lock()
		ms.startupErr = ""
		ms.mu.Unlock()

		// Create a fresh runner for each attempt
		if ms.cfg.IsContainer() {
			ms.r = runner.NewContainerRunner(ms.cfg, ms.key, s.cfg.Global.ContainerPrefix, s.backend)
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

			// A platform mismatch (e.g. an image with no arm64 variant) can
			// never succeed on retry — fail terminally instead of looping
			// through the restart policy.
			if errors.Is(err, runner.ErrUnsupportedPlatform) {
				return
			}

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

		// Start a readiness probe for this running instance. The probe runs
		// under a child context that is cancelled whenever the runLoop exits
		// the Running state (process exit, stop, restart, shutdown), so it
		// can never outlive its process. Services without a configured probe
		// transition to Ready immediately — runProbe returns true for an
		// empty/none kind — so Ready is the uniform "up and good to go" state.
		probeCtx, cancelProbe := context.WithCancel(s.ctx)
		var probeWG sync.WaitGroup
		var baseline uint64
		if last := ms.logs.Last(1); len(last) == 1 {
			baseline = last[0].Seq
		}
		// Resolved before the goroutine starts so startup hooks never race the
		// runLoop for ms.r. Process runners don't implement ContainerExecer,
		// which leaves execer nil and makes container_exec fail with a clear
		// message rather than silently probing nothing.
		execer, _ := ms.r.(runner.ContainerExecer)
		probeWG.Go(func() {
			if !runProbe(probeCtx, ms.cfg.Readiness, ms.logs, baseline, execer) {
				if probeCtx.Err() == nil {
					ms.mu.Lock()
					ms.startupErr = readinessFailureReason(ms.cfg.Readiness)
					ms.mu.Unlock()
					select {
					case ms.stopCh <- struct{}{}:
					default:
					}
				}
				return
			}
			if ms.cfg.Setup != nil {
				s.setStatus(ms, service.StatusSetup, "running setup hook")
				if err := s.runSetupHook(probeCtx, ms, execer); err != nil {
					// Record the failure on the managed service. The runLoop's
					// stop path sees startupErr and finalises as Failed (not
					// Stopped) so the user can tell apart "I stopped this"
					// from "its bootstrap exploded".
					ms.mu.Lock()
					ms.startupErr = fmt.Sprintf("setup hook: %v", err)
					ms.mu.Unlock()
					select {
					case ms.stopCh <- struct{}{}:
					default:
					}
					return
				}
			}
			var reason string
			if kind := ms.cfg.Readiness.Kind; kind != "" && kind != "none" {
				reason = "readiness check passed"
			}
			s.setStatus(ms, service.StatusReady, reason)
		})
		stopProbe := func() {
			cancelProbe()
			probeWG.Wait()
		}

		select {
		case exitCode := <-exitCh:
			stopProbe()
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
			stopProbe()
			ms.mu.Lock()
			startupErr := ms.startupErr
			ms.startupErr = ""
			ms.mu.Unlock()

			s.setStatus(ms, service.StatusStopping, "stopping")
			ms.r.Stop(exitCh, timeout)
			ms.info.Lock()
			ms.info.PID = 0
			ms.info.ContainerID = ""
			ms.info.StopTime = time.Now()
			ms.info.Unlock()
			if startupErr != "" {
				// Internal stop: readiness/setup failed. Surface Failed with the
				// real cause so dependents cascade and the user sees why.
				s.setStatus(ms, service.StatusFailed, startupErr)
			} else {
				s.setStatus(ms, service.StatusStopped, "stopped")
			}
			return

		case reason := <-ms.restartCh:
			stopProbe()
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
			stopProbe()
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

func readinessFailureReason(cfg config.ServiceHookConfig) string {
	if cfg.MaxAttempts > 0 {
		return fmt.Sprintf("readiness probe failed after %d attempts", cfg.MaxAttempts)
	}
	if cfg.Kind != "" && cfg.Kind != "none" {
		return "readiness probe failed"
	}
	return "readiness failed"
}

// depSatisfied reports whether a dep's current status satisfies this
// dependent's start condition, and whether the dep has terminated (Failed or
// Stopped) so the caller should cascade-fail.
//
// All running services ultimately reach StatusReady — services without a
// probe are promoted instantly by the runLoop — so Ready is the single
// "healthy" signal. Disabled is treated as satisfied so that a dep with
// auto_start:false does not deadlock its dependents.
func depSatisfied(status service.Status) (satisfied, terminal bool) {
	switch status {
	case service.StatusReady, service.StatusDisabled:
		return true, false
	case service.StatusFailed, service.StatusStopped:
		return false, true
	}
	return false, false
}

// waitForDependencies blocks until every service in ms.cfg.DependsOn is
// satisfied (see depSatisfied). If a dependency terminates before becoming
// satisfied, the current service is marked Failed and the function returns
// false so the run loop exits without starting.
//
// Returns false if the caller should abandon startup (ctx cancelled, stop
// requested, or a dependency failed).
func (s *Supervisor) waitForDependencies(ms *managedService) bool {
	if len(ms.cfg.DependsOn) == 0 {
		return true
	}

	pendingAnnounced := false
	for _, depKey := range ms.cfg.DependsOn {
		depMs, ok := s.services[depKey]
		if !ok {
			// Unknown deps are rejected by config.Validate; treat as satisfied.
			continue
		}

		for {
			depMs.info.RLock()
			depStatus := depMs.info.Status
			depMs.info.RUnlock()

			satisfied, terminal := depSatisfied(depStatus)
			if satisfied {
				break
			}
			if terminal {
				s.setStatus(ms, service.StatusFailed,
					fmt.Sprintf("dependency %q is %s", depKey, depStatus.String()))
				return false
			}

			if !pendingAnnounced {
				s.setStatus(ms, service.StatusPending,
					fmt.Sprintf("waiting for: %s", strings.Join(ms.cfg.DependsOn, ", ")))
				pendingAnnounced = true
			}

			select {
			case <-s.ctx.Done():
				return false
			case <-ms.stopCh:
				s.setStatus(ms, service.StatusStopped, "stopped while waiting for dependencies")
				return false
			case <-time.After(dependencyPollInterval):
			}
		}
	}
	return true
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

	// Global inline env (overrides env_file, overridden by per-service env)
	for k, v := range s.cfg.Global.Env {
		env = append(env, k+"="+v)
	}

	// Load service env file
	if ms.cfg.EnvFile != "" {
		fileEnv, err := config.LoadEnvFile(ms.cfg.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("env_file %q: %w", ms.cfg.EnvFile, err)
		}
		env = append(env, fileEnv...)
	}

	// OTEL env var injection when tracing is enabled. These are the
	// lowest-precedence source: they only fill in defaults when the variable
	// is not already set by any other layer (OS env, global env_file, global
	// inline env, service env_file, or service inline env). The service inline
	// env is appended after this block, so it is checked separately via the
	// config map; everything else is already present in env by this point.
	if s.cfg.Global.Tracing.Enabled {
		port := s.cfg.Global.Tracing.Port
		alreadySet := func(key string) bool {
			if _, ok := ms.cfg.Env[key]; ok {
				return true
			}
			prefix := key + "="
			for _, e := range env {
				if strings.HasPrefix(e, prefix) {
					return true
				}
			}
			return false
		}
		// The collector listens on the host. Host-process services reach it
		// via localhost, but container services have their own loopback, so
		// they reach the host collector via the backend's host address
		// (host.docker.internal for Docker, the vmnet gateway IP for Apple).
		otelHost := "localhost"
		if ms.cfg.IsContainer() {
			otelHost = s.backend.OTELHost()
		}
		if !alreadySet("OTEL_EXPORTER_OTLP_ENDPOINT") {
			env = append(env, fmt.Sprintf("OTEL_EXPORTER_OTLP_ENDPOINT=http://%s:%d", otelHost, port))
		}
		if !alreadySet("OTEL_EXPORTER_OTLP_PROTOCOL") {
			env = append(env, "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf")
		}
	}

	// Inline env (highest priority)
	for k, v := range ms.cfg.Env {
		env = append(env, k+"="+v)
	}

	return env, nil
}
