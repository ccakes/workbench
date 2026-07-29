package config

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
)

var invalidContainerPrefixChar = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

type ValidationError struct {
	Errors []string
}

func (e *ValidationError) Error() string {
	return strings.Join(e.Errors, "\n")
}

func (c *Config) Validate() error {
	var errs []string

	if c.Version != 1 {
		errs = append(errs, fmt.Sprintf("unsupported config version: %d (expected 1)", c.Version))
	}

	if len(c.Services) == 0 {
		errs = append(errs, "no services defined")
	}

	for key, svc := range c.Services {
		prefix := fmt.Sprintf("service %q", key)

		hasCommand := svc.Command != nil && len(svc.Command.Parts) > 0
		hasContainer := svc.Container != nil

		if hasCommand && hasContainer {
			errs = append(errs, fmt.Sprintf("%s: cannot have both command and container", prefix))
		} else if !hasCommand && !hasContainer {
			errs = append(errs, fmt.Sprintf("%s: must have either command or container", prefix))
		}

		if hasContainer {
			// Container-specific validation
			if svc.Container.Image == "" {
				errs = append(errs, fmt.Sprintf("%s: container.image is required", prefix))
			}
			for _, p := range svc.Container.Ports {
				if !strings.Contains(p, ":") {
					errs = append(errs, fmt.Sprintf("%s: container port %q must contain ':'", prefix, p))
				}
			}
			if svc.Watch.IsEnabled() {
				errs = append(errs, fmt.Sprintf("%s: watch is not supported for container services", prefix))
			}
			// dir is optional for containers
			if svc.Dir != "" {
				if info, err := os.Stat(svc.Dir); err != nil {
					errs = append(errs, fmt.Sprintf("%s: dir %q does not exist", prefix, svc.Dir))
				} else if !info.IsDir() {
					errs = append(errs, fmt.Sprintf("%s: dir %q is not a directory", prefix, svc.Dir))
				}
			}
		} else if hasCommand {
			// Process-specific validation
			if svc.Dir == "" {
				errs = append(errs, fmt.Sprintf("%s: dir is required", prefix))
			} else if info, err := os.Stat(svc.Dir); err != nil {
				errs = append(errs, fmt.Sprintf("%s: dir %q does not exist", prefix, svc.Dir))
			} else if !info.IsDir() {
				errs = append(errs, fmt.Sprintf("%s: dir %q is not a directory", prefix, svc.Dir))
			}
		}

		switch svc.Restart.Policy {
		case "never", "on-failure", "always":
			// valid
		default:
			errs = append(errs, fmt.Sprintf("%s: invalid restart policy %q (must be never, on-failure, or always)", prefix, svc.Restart.Policy))
		}

		if svc.EnvFile != "" {
			if _, err := os.Stat(svc.EnvFile); err != nil {
				errs = append(errs, fmt.Sprintf("%s: env_file %q could not be read: %v", prefix, svc.EnvFile, err))
			}
		}

		for _, dep := range svc.DependsOn {
			if _, ok := c.Services[dep]; !ok {
				errs = append(errs, fmt.Sprintf("%s: depends_on references unknown service %q", prefix, dep))
			}
		}

		switch svc.Readiness.Kind {
		case "", "none", "log_pattern", "tcp", "http", "exec", "container_exec", "grpc":
			// valid
		default:
			errs = append(errs, fmt.Sprintf("%s: invalid readiness kind %q", prefix, svc.Readiness.Kind))
		}

		if svc.Readiness.Kind == "log_pattern" && svc.Readiness.Pattern == "" {
			errs = append(errs, fmt.Sprintf("%s: readiness kind log_pattern requires a pattern", prefix))
		}
		if svc.Readiness.Kind == "tcp" && svc.Readiness.Address == "" {
			errs = append(errs, fmt.Sprintf("%s: readiness kind tcp requires an address", prefix))
		}
		if svc.Readiness.Kind == "http" && svc.Readiness.URL == "" {
			errs = append(errs, fmt.Sprintf("%s: readiness kind http requires a url", prefix))
		}
		if svc.Readiness.Kind == "exec" && (svc.Readiness.Command == nil || len(svc.Readiness.Command.Parts) == 0) {
			errs = append(errs, fmt.Sprintf("%s: readiness kind exec requires a command", prefix))
		}
		if svc.Readiness.Kind == "container_exec" {
			if svc.Readiness.Command == nil || len(svc.Readiness.Command.Parts) == 0 {
				errs = append(errs, fmt.Sprintf("%s: readiness kind container_exec requires a command", prefix))
			}
			// The probe runs inside the service's own container, so there has to
			// be one. Caught here rather than at runtime because it can only ever
			// be a config mistake.
			if !svc.IsContainer() {
				errs = append(errs, fmt.Sprintf("%s: readiness kind container_exec requires a container service", prefix))
			}
		}
		if svc.Readiness.Kind == "grpc" && svc.Readiness.Address == "" {
			errs = append(errs, fmt.Sprintf("%s: readiness kind grpc requires an address", prefix))
		}
		if svc.Readiness.MaxAttempts < 0 {
			errs = append(errs, fmt.Sprintf("%s: readiness max_attempts must be >= 0", prefix))
		}
		if svc.Readiness.Interval.Duration < 0 {
			errs = append(errs, fmt.Sprintf("%s: readiness interval must be >= 0", prefix))
		}
		if svc.Readiness.Settle.Duration < 0 {
			errs = append(errs, fmt.Sprintf("%s: readiness settle must be >= 0", prefix))
		}

		if svc.Setup != nil {
			if len(svc.Setup.Command.Parts) == 0 {
				errs = append(errs, fmt.Sprintf("%s: setup requires a command", prefix))
			}
			if svc.Setup.Timeout.Duration < 0 {
				errs = append(errs, fmt.Sprintf("%s: setup timeout must be >= 0", prefix))
			}
		}
	}

	if err := c.checkCycles(); err != nil {
		errs = append(errs, err.Error())
	}

	if bad := invalidContainerPrefixChar.FindString(c.Global.ContainerPrefix); bad != "" {
		errs = append(errs, fmt.Sprintf("container_prefix %q contains invalid character %q (only alphanumeric, hyphens, and underscores are allowed)", c.Global.ContainerPrefix, bad))
	}

	switch c.Global.ContainerBackend {
	case "", BackendDocker, BackendApple, BackendAuto:
		// valid (empty is defaulted to auto)
	default:
		errs = append(errs, fmt.Sprintf("invalid container_backend %q (must be %q, %q, or %q)", c.Global.ContainerBackend, BackendDocker, BackendApple, BackendAuto))
	}

	if c.Global.Apple.GatewayIP != "" && net.ParseIP(c.Global.Apple.GatewayIP) == nil {
		errs = append(errs, fmt.Sprintf("apple.gateway_ip %q is not a valid IP address", c.Global.Apple.GatewayIP))
	}

	if c.Global.EnvFile != "" {
		if _, err := os.Stat(c.Global.EnvFile); err != nil {
			errs = append(errs, fmt.Sprintf("global env_file %q: %v", c.Global.EnvFile, err))
		}
	}

	if c.Global.Tracing.Enabled {
		if c.Global.Tracing.Port <= 0 || c.Global.Tracing.Port >= 65536 {
			errs = append(errs, fmt.Sprintf("tracing port must be between 1 and 65535, got %d", c.Global.Tracing.Port))
		}
		if c.Global.Tracing.BufferSize <= 0 {
			errs = append(errs, "tracing buffer_size must be greater than 0")
		}
	}

	if len(errs) > 0 {
		return &ValidationError{Errors: errs}
	}
	return nil
}

func (c *Config) checkCycles() error {
	type color int
	const (
		white color = iota
		gray
		black
	)
	colors := make(map[string]color)
	for key := range c.Services {
		colors[key] = white
	}

	var visit func(string, []string) error
	visit = func(node string, path []string) error {
		colors[node] = gray
		path = append(path, node)
		svc := c.Services[node]
		for _, dep := range svc.DependsOn {
			switch colors[dep] {
			case gray:
				return fmt.Errorf("dependency cycle detected: %s -> %s", strings.Join(path, " -> "), dep)
			case white:
				if err := visit(dep, path); err != nil {
					return err
				}
			}
		}
		colors[node] = black
		return nil
	}

	for key := range c.Services {
		if colors[key] == white {
			if err := visit(key, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// TransitiveDeps returns the set of services reachable from roots via
// depends_on, including the roots themselves. Unknown roots are reported as
// an error. The result is a map for cheap membership tests; callers can
// intersect it with StartOrder() to get a launch order.
func (c *Config) TransitiveDeps(roots []string) (map[string]bool, error) {
	set := make(map[string]bool, len(roots))
	var visit func(string) error
	visit = func(key string) error {
		if set[key] {
			return nil
		}
		svc, ok := c.Services[key]
		if !ok {
			return fmt.Errorf("unknown service %q", key)
		}
		set[key] = true
		for _, dep := range svc.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range roots {
		if err := visit(r); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// StartOrder returns service keys in dependency-respecting start order
// (dependencies first).
func (c *Config) StartOrder() ([]string, error) {
	if err := c.checkCycles(); err != nil {
		return nil, err
	}

	visited := make(map[string]bool)
	var order []string

	var visit func(string)
	visit = func(key string) {
		if visited[key] {
			return
		}
		visited[key] = true
		svc := c.Services[key]
		for _, dep := range svc.DependsOn {
			visit(dep)
		}
		order = append(order, key)
	}

	keys := sortedKeys(c.Services)
	for _, key := range keys {
		visit(key)
	}
	return order, nil
}

func sortedKeys(m map[string]ServiceConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}
