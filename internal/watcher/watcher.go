package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"

	"github.com/ccakes/workbench/internal/config"
	"github.com/ccakes/workbench/internal/events"
	"github.com/ccakes/workbench/internal/service"
)

// Restarter is the interface for triggering service restarts.
type Restarter interface {
	RestartService(key, reason string) error
	ServiceInfo(key string) *service.Info
}

type Manager struct {
	cfg       *config.Config
	restarter Restarter
	bus       *events.Bus
	watchers  map[string]*serviceWatcher
	ctx       context.Context
	cancel    context.CancelFunc
}

type serviceWatcher struct {
	key       string
	svcCfg    config.ServiceConfig
	fsWatcher *fsnotify.Watcher
	debounce  time.Duration
	mu        sync.Mutex
	timer     *time.Timer
	lastFile  string
}

func NewManager(cfg *config.Config, restarter Restarter, bus *events.Bus) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:       cfg,
		restarter: restarter,
		bus:       bus,
		watchers:  make(map[string]*serviceWatcher),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start begins watching for all services that have watch enabled.
func (m *Manager) Start() error {
	for key, svcCfg := range m.cfg.Services {
		if !svcCfg.Watch.IsEnabled() {
			continue
		}
		if err := m.addServiceWatcher(key, svcCfg); err != nil {
			return err
		}
	}
	return nil
}

// Stop shuts down all watchers.
func (m *Manager) Stop() {
	m.cancel()
	for _, sw := range m.watchers {
		_ = sw.fsWatcher.Close()
	}
}

// IsWatching returns whether a service is being watched.
func (m *Manager) IsWatching(key string) bool {
	_, ok := m.watchers[key]
	return ok
}

func (m *Manager) addServiceWatcher(key string, svcCfg config.ServiceConfig) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	sw := &serviceWatcher{
		key:      key,
		svcCfg:   svcCfg,
		fsWatcher: fsw,
		debounce: svcCfg.Watch.GetDebounce(m.cfg.Global.WatchDebounce),
	}

	// Add watch directories
	for _, p := range svcCfg.Watch.Paths {
		root := p
		if !filepath.IsAbs(root) {
			root = filepath.Join(svcCfg.Dir, root)
		}
		if err := addDirsRecursive(fsw, root, svcCfg.Watch.Ignore); err != nil {
			_ = fsw.Close()
			return err
		}
	}

	m.watchers[key] = sw
	go m.watchLoop(sw)
	return nil
}

func (m *Manager) watchLoop(sw *serviceWatcher) {
	for {
		select {
		case <-m.ctx.Done():
			return
		case evt, ok := <-sw.fsWatcher.Events:
			if !ok {
				return
			}
			if evt.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}

			// Handle new directories
			if evt.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(evt.Name); err == nil && info.IsDir() {
					_ = addDirsRecursive(sw.fsWatcher, evt.Name, sw.svcCfg.Watch.Ignore)
				}
			}

			// Check if the service still has watch enabled
			info := m.restarter.ServiceInfo(sw.key)
			if info != nil {
				info.RLock()
				enabled := info.WatchEnabled
				info.RUnlock()
				if !enabled {
					continue
				}
			}

			relPath := relativePath(evt.Name, sw.svcCfg.Dir)

			if matchesAny(relPath, sw.svcCfg.Watch.Ignore) {
				continue
			}

			if len(sw.svcCfg.Watch.Include) > 0 && !matchesAny(relPath, sw.svcCfg.Watch.Include) {
				continue
			}

			if !sw.svcCfg.Watch.ShouldRestart() {
				continue
			}

			m.bus.Publish(events.Event{
				Type:    events.FileChanged,
				Service: sw.key,
				Data:    events.FileChangeData{Path: relPath},
			})

			sw.scheduleRestart(m, relPath)

		case err, ok := <-sw.fsWatcher.Errors:
			if !ok {
				return
			}
			_ = err // fsnotify errors are non-fatal
		}
	}
}

func (sw *serviceWatcher) scheduleRestart(m *Manager, file string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.lastFile = file
	if sw.timer != nil {
		sw.timer.Stop()
	}
	sw.timer = time.AfterFunc(sw.debounce, func() {
		sw.mu.Lock()
		reason := fmt.Sprintf("file changed: %s", sw.lastFile)
		sw.mu.Unlock()
		_ = m.restarter.RestartService(sw.key, reason)
	})
}

func addDirsRecursive(fsw *fsnotify.Watcher, root string, ignore []string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}
		if !d.IsDir() {
			return nil
		}
		rel := relativePath(path, root)
		if rel != "." && matchesAny(rel+"/", ignore) {
			return filepath.SkipDir
		}
		// Skip common noisy directories
		name := d.Name()
		if name == ".git" || name == "node_modules" || name == ".next" || name == "__pycache__" {
			return filepath.SkipDir
		}
		return fsw.Add(path)
	})
}

func relativePath(path, base string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

func matchesAny(path string, patterns []string) bool {
	for _, p := range patterns {
		if matched, _ := doublestar.Match(p, path); matched {
			return true
		}
		// Also try with filepath separator normalization
		normalized := filepath.ToSlash(path)
		if matched, _ := doublestar.Match(p, normalized); matched {
			return true
		}
	}
	return false
}
