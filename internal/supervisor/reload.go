package supervisor

import (
	"fmt"
	"reflect"

	"github.com/ccakes/workbench/internal/config"
)

// ReloadReport summarises the per-service outcome of a Reload call.
type ReloadReport struct {
	Added      []string          `json:"added"`
	Removed    []string          `json:"removed"`
	Restarted  []string          `json:"restarted"`
	Unchanged  []string          `json:"unchanged"`
	Errors     map[string]string `json:"errors,omitempty"`
	NeedsRerun bool              `json:"needs_rerun"` // true if Added/Removed is non-empty — a full bench up restart is required
}

// Reload diffs newCfg against the supervisor's current config and restarts
// services whose service-level config changed. New services (in newCfg but
// not currently managed) and removed services (managed but not in newCfg)
// are reported but not acted on — the supervisor's services map is built at
// construction and adding/removing entries safely under concurrent access
// would require a broader lock refactor. Until then, the caller must
// `bench down && bench up` to pick up structural changes.
//
// Returns a ReloadReport summarising what changed. Per-service errors
// (e.g. restart failures) are collected in Errors rather than aborting.
func (s *Supervisor) Reload(newCfg *config.Config) ReloadReport {
	report := ReloadReport{Errors: map[string]string{}}

	for key, ms := range s.services {
		newSvc, present := newCfg.Services[key]
		if !present {
			report.Removed = append(report.Removed, key)
			report.NeedsRerun = true
			continue
		}
		if servicesEqual(ms.cfg, newSvc) {
			report.Unchanged = append(report.Unchanged, key)
			continue
		}
		// In-place cfg update. ms.mu guards both `running` and now `cfg`;
		// runLoop reads cfg on every loop pass after restartCh fires.
		ms.mu.Lock()
		ms.cfg = newSvc
		ms.mu.Unlock()
		applyServiceMetadata(ms.info, key, newSvc, true)
		if err := s.RestartService(key, "config reload"); err != nil {
			report.Errors[key] = err.Error()
			continue
		}
		report.Restarted = append(report.Restarted, key)
	}

	for key := range newCfg.Services {
		if _, ok := s.services[key]; !ok {
			report.Added = append(report.Added, key)
			report.NeedsRerun = true
		}
	}

	// Replace the supervisor's cfg pointer so subsequent ServiceConfig()
	// calls reflect the reloaded globals. Race with concurrent readers is
	// limited to the pointer write itself, which is atomic on supported
	// architectures.
	s.cfg = newCfg

	if len(report.Errors) == 0 {
		report.Errors = nil
	}
	return report
}

// servicesEqual returns true when two ServiceConfigs are functionally
// identical — i.e. a restart wouldn't change the running service's behaviour.
// reflect.DeepEqual on the value handles every field including nested
// structs and slices; future config additions are picked up automatically.
func servicesEqual(a, b config.ServiceConfig) bool {
	return reflect.DeepEqual(a, b)
}

// summary describes what a ReloadReport implies for the operator. Used by
// the CLI to print a human-friendly outcome line.
func (r ReloadReport) Summary() string {
	if r.NeedsRerun {
		return fmt.Sprintf(
			"reload partial: %d restarted, %d unchanged, %d added, %d removed (full `bench up` required for structural changes)",
			len(r.Restarted), len(r.Unchanged), len(r.Added), len(r.Removed),
		)
	}
	return fmt.Sprintf("reload complete: %d restarted, %d unchanged", len(r.Restarted), len(r.Unchanged))
}
