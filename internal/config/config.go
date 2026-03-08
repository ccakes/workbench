package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version  int                      `yaml:"version"`
	Global   GlobalConfig             `yaml:"global"`
	Services map[string]ServiceConfig `yaml:"services"`
}

type GlobalConfig struct {
	ShutdownTimeout Duration      `yaml:"shutdown_timeout"`
	LogBufferLines  int           `yaml:"log_buffer_lines"`
	WatchDebounce   Duration      `yaml:"watch_debounce"`
	EnvFile         string        `yaml:"env_file"`
	Tracing         TracingConfig `yaml:"tracing"`
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
	Image   string  `yaml:"image"`
	Ports   []string `yaml:"ports"`
	Volumes []string `yaml:"volumes"`
	Network string  `yaml:"network"`
	Command Command `yaml:"command"`
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

// Load reads and parses a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	return Parse(data, filepath.Dir(path))
}

// Parse parses YAML config data. Relative paths are resolved against baseDir.
func Parse(data []byte, baseDir string) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	cfg.applyDefaults()
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
	return &cfg, nil
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
