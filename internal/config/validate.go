package config

import (
	"fmt"
	"os"
	"strings"
)

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
		case "", "none", "log_pattern", "tcp", "http":
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
	}

	if err := c.checkCycles(); err != nil {
		errs = append(errs, err.Error())
	}

	if c.Global.ContainerPrefix != "" {
		for _, ch := range c.Global.ContainerPrefix {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
				errs = append(errs, fmt.Sprintf("container_prefix %q contains invalid character %q (only alphanumeric, hyphens, and underscores are allowed)", c.Global.ContainerPrefix, string(ch)))
				break
			}
		}
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
