package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version  int                      `yaml:"version"`
	Extends  string                   `yaml:"extends"`
	Global   GlobalConfig             `yaml:"global"`
	Services map[string]ServiceConfig `yaml:"services"`
}

type GlobalConfig struct {
	ShutdownTimeout Duration          `yaml:"shutdown_timeout"`
	LogBufferLines  int               `yaml:"log_buffer_lines"`
	WatchDebounce   Duration          `yaml:"watch_debounce"`
	Env             map[string]string `yaml:"env"`
	EnvFile         string            `yaml:"env_file"`
	ContainerPrefix string            `yaml:"container_prefix"`
	Tracing         TracingConfig     `yaml:"tracing"`
}

// Duration wraps time.Duration for YAML unmarshaling from strings like "10s".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = dur
	return nil
}

func (d Duration) MarshalYAML() (interface{}, error) {
	return d.String(), nil
}

// Command handles the YAML command field being either a string or string array.
type Command struct {
	Shell bool
	Parts []string
}

func (c *Command) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err == nil {
		c.Shell = true
		c.Parts = []string{"sh", "-c", s}
		return nil
	}
	var arr []string
	if err := value.Decode(&arr); err == nil {
		if len(arr) == 0 {
			return fmt.Errorf("command array must not be empty")
		}
		c.Parts = arr
		return nil
	}
	return fmt.Errorf("command must be a string or array of strings")
}

func (c Command) String() string {
	if c.Shell && len(c.Parts) == 3 {
		return c.Parts[2]
	}
	if len(c.Parts) == 1 {
		return c.Parts[0]
	}
	return fmt.Sprintf("%v", c.Parts)
}

type ContainerConfig struct {
	Image   string   `yaml:"image"`
	Ports   []string `yaml:"ports"`
	Volumes []string `yaml:"volumes"`
	Network string   `yaml:"network"`
	Command Command  `yaml:"command"`
}

type ServiceConfig struct {
	Name            string            `yaml:"name"`
	Dir             string            `yaml:"dir"`
	Command         *Command          `yaml:"command"`
	Container       *ContainerConfig  `yaml:"container"`
	Env             map[string]string `yaml:"env"`
	EnvFile         string            `yaml:"env_file"`
	AutoStart       *bool             `yaml:"auto_start"`
	DependsOn       []string          `yaml:"depends_on"`
	Restart         RestartConfig     `yaml:"restart"`
	Watch           WatchConfig       `yaml:"watch"`
	Readiness       ReadinessConfig   `yaml:"readiness"`
	Labels          map[string]string `yaml:"labels"`
	StopSignal      string            `yaml:"stop_signal"`
	ShutdownTimeout *Duration         `yaml:"shutdown_timeout"`
}

// IsContainer returns true if this service is a container service.
func (s *ServiceConfig) IsContainer() bool {
	return s.Container != nil
}

func (s *ServiceConfig) GetAutoStart() bool {
	if s.AutoStart == nil {
		return true
	}
	return *s.AutoStart
}

func (s *ServiceConfig) GetShutdownTimeout(global Duration) time.Duration {
	if s.ShutdownTimeout != nil {
		return s.ShutdownTimeout.Duration
	}
	if global.Duration > 0 {
		return global.Duration
	}
	return 10 * time.Second
}

type RestartConfig struct {
	Policy        string   `yaml:"policy"`
	MaxRetries    int      `yaml:"max_retries"`
	Backoff       Duration `yaml:"backoff"`
	SuccessWindow Duration `yaml:"success_window"`
}

type WatchConfig struct {
	Enabled  *bool     `yaml:"enabled"`
	Paths    []string  `yaml:"paths"`
	Include  []string  `yaml:"include"`
	Ignore   []string  `yaml:"ignore"`
	Debounce *Duration `yaml:"debounce"`
	Restart  *bool     `yaml:"restart"`
}

func (w *WatchConfig) IsEnabled() bool {
	if w.Enabled == nil {
		return false
	}
	return *w.Enabled
}

func (w *WatchConfig) GetDebounce(global Duration) time.Duration {
	if w.Debounce != nil && w.Debounce.Duration > 0 {
		return w.Debounce.Duration
	}
	if global.Duration > 0 {
		return global.Duration
	}
	return 300 * time.Millisecond
}

func (w *WatchConfig) ShouldRestart() bool {
	if w.Restart == nil {
		return true
	}
	return *w.Restart
}

type ReadinessConfig struct {
	Kind         string   `yaml:"kind"`
	Pattern      string   `yaml:"pattern"`
	Address      string   `yaml:"address"`
	URL          string   `yaml:"url"`
	Timeout      Duration `yaml:"timeout"`
	InitialDelay Duration `yaml:"initial_delay"`
}

type TracingConfig struct {
	Enabled    bool     `yaml:"enabled"`
	Port       int      `yaml:"port"`
	BufferSize ByteSize `yaml:"buffer_size"`
}

// ByteSize parses human-readable byte sizes like "500MB", "1GB".
type ByteSize int64

func (b *ByteSize) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := parseByteSize(s)
	if err != nil {
		return err
	}
	*b = ByteSize(parsed)
	return nil
}

func (b ByteSize) MarshalYAML() (interface{}, error) {
	return formatByteSize(int64(b)), nil
}

func parseByteSize(s string) (int64, error) {
	s = trimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty byte size")
	}
	// Find where digits end and suffix begins
	i := 0
	for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
		i++
	}
	numStr := s[:i]
	suffix := s[i:]
	if numStr == "" {
		return 0, fmt.Errorf("invalid byte size %q: no number", s)
	}

	// Parse as float to handle decimals like "1.5GB"
	var num float64
	for j, c := range numStr {
		if c == '.' {
			intPart := numStr[:j]
			fracPart := numStr[j+1:]
			ip := parseUint(intPart)
			fp := parseUint(fracPart)
			divisor := 1.0
			for range fracPart {
				divisor *= 10
			}
			num = float64(ip) + float64(fp)/divisor
			break
		}
		if j == len(numStr)-1 {
			num = float64(parseUint(numStr))
		}
	}

	// Normalize suffix
	suffix = trimSpace(suffix)
	upper := ""
	for _, c := range suffix {
		if c >= 'a' && c <= 'z' {
			upper += string(c - 32)
		} else {
			upper += string(c)
		}
	}

	var multiplier int64
	switch upper {
	case "", "B":
		multiplier = 1
	case "KB", "K":
		multiplier = 1024
	case "MB", "M":
		multiplier = 1024 * 1024
	case "GB", "G":
		multiplier = 1024 * 1024 * 1024
	case "TB", "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("invalid byte size suffix %q in %q", suffix, s)
	}

	return int64(num * float64(multiplier)), nil
}

func parseUint(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}

func formatByteSize(b int64) string {
	switch {
	case b >= 1024*1024*1024*1024:
		return fmt.Sprintf("%dTB", b/(1024*1024*1024*1024))
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%dGB", b/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%dMB", b/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%dKB", b/1024)
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// Load reads and parses a config file. If the file declares `extends:`, the
// referenced parent is loaded recursively and merged underneath the child.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving config path %s: %w", path, err)
	}
	cfg, err := loadWithStack(abs, nil)
	if err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	if cfg.Global.ContainerPrefix == "" {
		cfg.Global.ContainerPrefix = filepath.Base(filepath.Dir(abs))
	}
	return cfg, nil
}

// Parse parses YAML config data for a single file. Relative paths are resolved
// against baseDir. `extends:` is rejected here — use Load for files that may
// reference a parent.
func Parse(data []byte, baseDir string) (*Config, error) {
	cfg, err := parseRaw(data, baseDir)
	if err != nil {
		return nil, err
	}
	if cfg.Extends != "" {
		return nil, fmt.Errorf("Parse does not support 'extends:'; use Load")
	}
	cfg.applyDefaults()
	if cfg.Global.ContainerPrefix == "" {
		cfg.Global.ContainerPrefix = filepath.Base(baseDir)
	}
	return cfg, nil
}

// parseRaw unmarshals YAML and resolves relative paths against baseDir. It
// does not apply defaults or resolve `extends:`.
//
// Unknown fields are rejected via KnownFields(true). Silent typos in config
// (e.g. `expect_status` under a readiness block) used to be ignored; they now
// fail loudly so the user discovers the mistake at validate time instead of
// at runtime when the misconfigured feature silently does nothing.
func parseRaw(data []byte, baseDir string) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	resolveRelativePaths(&cfg, baseDir)
	return &cfg, nil
}

func resolveRelativePaths(cfg *Config, baseDir string) {
	for key, svc := range cfg.Services {
		if svc.Dir != "" && !filepath.IsAbs(svc.Dir) {
			svc.Dir = filepath.Join(baseDir, svc.Dir)
		}
		if svc.EnvFile != "" && !filepath.IsAbs(svc.EnvFile) {
			svc.EnvFile = filepath.Join(baseDir, svc.EnvFile)
		}
		if svc.Container != nil {
			for i, v := range svc.Container.Volumes {
				// Volumes are "host:container" — resolve host part if relative
				parts := strings.SplitN(v, ":", 2)
				if len(parts) == 2 && !filepath.IsAbs(parts[0]) {
					parts[0] = filepath.Join(baseDir, parts[0])
					svc.Container.Volumes[i] = parts[0] + ":" + parts[1]
				}
			}
		}
		cfg.Services[key] = svc
	}
	if cfg.Global.EnvFile != "" && !filepath.IsAbs(cfg.Global.EnvFile) {
		cfg.Global.EnvFile = filepath.Join(baseDir, cfg.Global.EnvFile)
	}
}

func loadWithStack(absPath string, stack []string) (*Config, error) {
	for _, s := range stack {
		if s == absPath {
			chain := append(append([]string{}, stack...), absPath)
			return nil, fmt.Errorf("extends: cycle detected: %s", strings.Join(chain, " -> "))
		}
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", absPath, err)
	}
	cfg, err := parseRaw(data, filepath.Dir(absPath))
	if err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", absPath, err)
	}
	if cfg.Extends == "" {
		return cfg, nil
	}
	parentPath := cfg.Extends
	if !filepath.IsAbs(parentPath) {
		parentPath = filepath.Join(filepath.Dir(absPath), parentPath)
	}
	parentAbs, err := filepath.Abs(parentPath)
	if err != nil {
		return nil, fmt.Errorf("resolving parent path %s (extended by %s): %w", parentPath, absPath, err)
	}
	parent, err := loadWithStack(parentAbs, append(stack, absPath))
	if err != nil {
		return nil, err
	}
	return merge(parent, cfg)
}

// merge combines a parent config with a child config. Child fields override
// parent fields where set; service maps are unioned and conflicts are an error.
func merge(parent, child *Config) (*Config, error) {
	out := *parent
	if child.Version != 0 {
		out.Version = child.Version
	}
	out.Global = mergeGlobal(parent.Global, child.Global)

	services := make(map[string]ServiceConfig, len(parent.Services)+len(child.Services))
	for k, v := range parent.Services {
		services[k] = v
	}
	var conflicts []string
	for k, v := range child.Services {
		if _, dup := services[k]; dup {
			conflicts = append(conflicts, k)
			continue
		}
		services[k] = v
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return nil, fmt.Errorf("service name conflicts between child and parent config: %s", strings.Join(conflicts, ", "))
	}
	out.Services = services
	out.Extends = ""
	return &out, nil
}

func mergeGlobal(p, c GlobalConfig) GlobalConfig {
	out := p
	if c.ShutdownTimeout.Duration != 0 {
		out.ShutdownTimeout = c.ShutdownTimeout
	}
	if c.LogBufferLines != 0 {
		out.LogBufferLines = c.LogBufferLines
	}
	if c.WatchDebounce.Duration != 0 {
		out.WatchDebounce = c.WatchDebounce
	}
	if c.EnvFile != "" {
		out.EnvFile = c.EnvFile
	}
	if c.ContainerPrefix != "" {
		out.ContainerPrefix = c.ContainerPrefix
	}
	out.Tracing = mergeTracing(p.Tracing, c.Tracing)
	if len(c.Env) > 0 {
		merged := make(map[string]string, len(p.Env)+len(c.Env))
		for k, v := range p.Env {
			merged[k] = v
		}
		for k, v := range c.Env {
			merged[k] = v
		}
		out.Env = merged
	}
	return out
}

// mergeTracing applies a child tracing config over the parent. Tracing.Enabled
// is enable-only: a child cannot disable tracing that a parent enabled.
func mergeTracing(p, c TracingConfig) TracingConfig {
	out := p
	if c.Enabled {
		out.Enabled = true
	}
	if c.Port != 0 {
		out.Port = c.Port
	}
	if c.BufferSize != 0 {
		out.BufferSize = c.BufferSize
	}
	return out
}

func (c *Config) applyDefaults() {
	if c.Global.LogBufferLines == 0 {
		c.Global.LogBufferLines = 5000
	}
	if c.Global.ShutdownTimeout.Duration == 0 {
		c.Global.ShutdownTimeout.Duration = 10 * time.Second
	}
	if c.Global.WatchDebounce.Duration == 0 {
		c.Global.WatchDebounce.Duration = 300 * time.Millisecond
	}
	if c.Global.Tracing.Port == 0 {
		c.Global.Tracing.Port = 4318
	}
	if c.Global.Tracing.BufferSize == 0 {
		c.Global.Tracing.BufferSize = ByteSize(500 * 1024 * 1024)
	}
	for key, svc := range c.Services {
		if svc.Restart.Policy == "" {
			svc.Restart.Policy = "never"
		}
		if svc.Restart.Backoff.Duration == 0 {
			svc.Restart.Backoff.Duration = 1 * time.Second
		}
		if len(svc.Watch.Paths) == 0 && svc.Watch.IsEnabled() {
			svc.Watch.Paths = []string{"."}
		}
		c.Services[key] = svc
	}
}

// FindConfig searches for bench.yml in the current and parent directories.
func FindConfig() (string, error) {
	names := []string{"bench.yml", "bench.yaml"}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		for _, name := range names {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("no bench.yml found in current or parent directories")
}

// LoadEnvFile reads a .env-style file and returns KEY=VALUE pairs.
func LoadEnvFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env []string
	for _, line := range splitLines(string(data)) {
		line = trimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		// Strip optional "export " prefix
		if len(line) > 7 && line[:7] == "export " {
			line = line[7:]
		}
		if idx := indexByte(line, '='); idx > 0 {
			key := line[:idx]
			val := line[idx+1:]
			// Strip surrounding quotes from value
			if len(val) >= 2 {
				if (val[0] == '"' && val[len(val)-1] == '"') ||
					(val[0] == '\'' && val[len(val)-1] == '\'') {
					val = val[1 : len(val)-1]
				}
			}
			env = append(env, key+"="+val)
		}
	}
	return env, nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
