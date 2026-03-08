package service

import (
	"fmt"
	"sync"
	"time"
)

type Status int

const (
	StatusPending Status = iota
	StatusStarting
	StatusRunning
	StatusReady
	StatusStopping
	StatusStopped
	StatusFailed
	StatusRestarting
	StatusBackoff
	StatusDisabled
)

var statusNames = map[Status]string{
	StatusPending:    "pending",
	StatusStarting:   "starting",
	StatusRunning:    "running",
	StatusReady:      "ready",
	StatusStopping:   "stopping",
	StatusStopped:    "stopped",
	StatusFailed:     "failed",
	StatusRestarting: "restarting",
	StatusBackoff:    "backoff",
	StatusDisabled:   "disabled",
}

func (s Status) String() string {
	if name, ok := statusNames[s]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", int(s))
}

// IsRunning returns true if the service has an active process.
func (s Status) IsRunning() bool {
	return s == StatusRunning || s == StatusReady || s == StatusStarting
}

// Info holds the runtime state of a service. All fields are protected by mu.
type Info struct {
	mu           sync.RWMutex
	Key          string
	DisplayName  string
	Status       Status
	PID          int
	StartTime    time.Time
	StopTime     time.Time
	ExitCode     int
	RestartCount int
	LastRestart  string
	LastError    string
	WatchEnabled bool
	ServiceType  string // "process" or "container"
	ContainerID  string
	Image        string
	Ports        []string
}

func NewInfo(key, displayName string) *Info {
	return &Info{
		Key:         key,
		DisplayName: displayName,
		Status:      StatusPending,
	}
}

func (i *Info) Lock()    { i.mu.Lock() }
func (i *Info) Unlock()  { i.mu.Unlock() }
func (i *Info) RLock()   { i.mu.RLock() }
func (i *Info) RUnlock() { i.mu.RUnlock() }

func (i *Info) Uptime() time.Duration {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.StartTime.IsZero() {
		return 0
	}
	switch i.Status {
	case StatusRunning, StatusReady, StatusStarting:
		return time.Since(i.StartTime).Truncate(time.Second)
	default:
		if !i.StopTime.IsZero() {
			return i.StopTime.Sub(i.StartTime).Truncate(time.Second)
		}
		return 0
	}
}

func (i *Info) Name() string {
	if i.DisplayName != "" {
		return i.DisplayName
	}
	return i.Key
}

// Snapshot returns a copy of the info safe for reading without locks.
type Snapshot struct {
	Key          string
	DisplayName  string
	Status       Status
	PID          int
	StartTime    time.Time
	StopTime     time.Time
	ExitCode     int
	RestartCount int
	LastRestart  string
	LastError    string
	WatchEnabled bool
	ServiceType  string
	ContainerID  string
	Image        string
	Ports        []string
}

func (i *Info) Snapshot() Snapshot {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return Snapshot{
		Key:          i.Key,
		DisplayName:  i.DisplayName,
		Status:       i.Status,
		PID:          i.PID,
		StartTime:    i.StartTime,
		StopTime:     i.StopTime,
		ExitCode:     i.ExitCode,
		RestartCount: i.RestartCount,
		LastRestart:  i.LastRestart,
		LastError:    i.LastError,
		WatchEnabled: i.WatchEnabled,
		ServiceType:  i.ServiceType,
		ContainerID:  i.ContainerID,
		Image:        i.Image,
		Ports:        i.Ports,
	}
}

func (s Snapshot) Name() string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	return s.Key
}

func (s Snapshot) Uptime() time.Duration {
	if s.StartTime.IsZero() {
		return 0
	}
	switch s.Status {
	case StatusRunning, StatusReady, StatusStarting:
		return time.Since(s.StartTime).Truncate(time.Second)
	default:
		if !s.StopTime.IsZero() {
			return s.StopTime.Sub(s.StartTime).Truncate(time.Second)
		}
		return 0
	}
}
