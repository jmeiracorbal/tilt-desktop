// Package tilt manages project-scoped Tilt processes and reads their local API.
// It does not provision clusters, persist sessions, or modify a user's kubeconfig.
package tilt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrNotRunning = errors.New("Tilt session is not running")
	ErrNotFound   = errors.New("Tilt session not found")
	clusterName   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Config binds exactly one Tilt process to one project and one k3d cluster.
// The caller is responsible for providing a private kubeconfig for that cluster
// and allocating a distinct local HTTP port.
type Config struct {
	ProjectID   string
	Directory   string
	Kubeconfig  string
	ClusterName string
	Port        uint16
}

func (c Config) validate() error {
	if strings.TrimSpace(c.ProjectID) == "" {
		return errors.New("project ID is required")
	}
	if !filepath.IsAbs(c.Directory) {
		return errors.New("absolute project directory is required")
	}
	if !filepath.IsAbs(c.Kubeconfig) {
		return errors.New("absolute kubeconfig path is required")
	}
	if len(c.ClusterName) > 63 || !clusterName.MatchString(c.ClusterName) {
		return fmt.Errorf("invalid k3d cluster name %q", c.ClusterName)
	}
	if c.Port == 0 {
		return errors.New("nonzero Tilt HTTP port is required")
	}
	if info, err := os.Stat(c.Directory); err != nil {
		return fmt.Errorf("inspect project directory: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("project path %q is not a directory", c.Directory)
	}
	if info, err := os.Stat(filepath.Join(c.Directory, "Tiltfile")); err != nil {
		return fmt.Errorf("inspect Tiltfile: %w", err)
	} else if !info.Mode().IsRegular() {
		return errors.New("Tiltfile must be a regular file")
	}
	if info, err := os.Stat(c.Kubeconfig); err != nil {
		return fmt.Errorf("inspect kubeconfig: %w", err)
	} else if !info.Mode().IsRegular() {
		return errors.New("kubeconfig must be a regular file")
	}
	return nil
}

func (c Config) contextName() string { return "k3d-" + c.ClusterName }
func (c Config) tiltfile() string    { return filepath.Join(c.Directory, "Tiltfile") }

// Process represents one child process started by Tilt Desktop.
type Process interface {
	Interrupt() error
	Wait() error
}

// Launcher encapsulates Tilt invocation without introducing a shell.
type Launcher interface {
	Start(context.Context, Config) (Process, error)
	Run(context.Context, Config, ...string) ([]byte, error)
}

// Resource is the stable subset of Tilt's UIResource API used by the core.
type Resource struct {
	Name          string
	RuntimeStatus string
	UpdateStatus  string
}

type resourceList struct {
	Kind  string `json:"kind"`
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			RuntimeStatus string `json:"runtimeStatus"`
			UpdateStatus  string `json:"updateStatus"`
		} `json:"status"`
	} `json:"items"`
}

// State reports the observed state of a process started by this manager.
// Started does not imply Tilt completed initialization or workloads are ready.
type State struct {
	Running bool
	ExitErr error
}

type session struct {
	config   Config
	process  Process
	done     chan struct{}
	running  bool
	stopping bool
	exitErr  error
}

// Manager serializes commands within one process. It does not recover child
// processes across application restarts; persistence is a separate concern.
type Manager struct {
	mu       sync.Mutex
	launcher Launcher
	sessions map[string]*session
}

func NewManager(launcher Launcher) (*Manager, error) {
	if launcher == nil {
		return nil, errors.New("Tilt launcher is required")
	}
	return &Manager{launcher: launcher, sessions: make(map[string]*session)}, nil
}

// Start launches exactly one Tilt process per project. Repeated Start with
// identical configuration is idempotent while the process is running.
func (m *Manager) Start(ctx context.Context, config Config) error {
	if err := config.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.sessions[config.ProjectID]; ok && old.running {
		if old.stopping {
			return fmt.Errorf("project %q is stopping", config.ProjectID)
		}
		if old.config == config {
			return nil
		}
		return fmt.Errorf("project %q has a different running Tilt configuration", config.ProjectID)
	}
	for id, current := range m.sessions {
		if current.running && id != config.ProjectID && current.config.Port == config.Port {
			return fmt.Errorf("Tilt HTTP port %d is already assigned to project %q", config.Port, id)
		}
	}
	process, err := m.launcher.Start(ctx, config)
	if err != nil {
		return fmt.Errorf("start Tilt for project %q: %w", config.ProjectID, err)
	}
	if process == nil {
		return errors.New("Tilt launcher returned no process")
	}
	s := &session{config: config, process: process, done: make(chan struct{}), running: true}
	m.sessions[config.ProjectID] = s
	go func() {
		err := process.Wait()
		m.mu.Lock()
		s.running = false
		s.exitErr = err
		close(s.done)
		m.mu.Unlock()
	}()
	return nil
}

// Stop interrupts only the process started for this project. It never invokes
// 'tilt down', which would remove deployed resources rather than stop Tilt.
func (m *Manager) Stop(ctx context.Context, projectID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	s, exists := m.sessions[projectID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotFound, projectID)
	}
	if !s.running {
		m.mu.Unlock()
		return nil
	}
	if !s.stopping {
		if err := s.process.Interrupt(); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("interrupt Tilt for project %q: %w", projectID, err)
		}
		s.stopping = true
	}
	done := s.done
	m.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Status(projectID string) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, exists := m.sessions[projectID]
	if !exists {
		return State{}, fmt.Errorf("%w: %s", ErrNotFound, projectID)
	}
	return State{Running: s.running, ExitErr: s.exitErr}, nil
}

func (m *Manager) activeConfig(projectID string) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, exists := m.sessions[projectID]
	if !exists {
		return Config{}, fmt.Errorf("%w: %s", ErrNotFound, projectID)
	}
	if !s.running {
		return Config{}, fmt.Errorf("%w: %s", ErrNotRunning, projectID)
	}
	return s.config, nil
}

// Resources returns a snapshot of resources from this Tilt instance only.
func (m *Manager) Resources(ctx context.Context, projectID string) ([]Resource, error) {
	config, err := m.activeConfig(projectID)
	if err != nil {
		return nil, err
	}
	data, err := m.launcher.Run(ctx, config, "get", "uiresources", "-o", "json", "--host", "127.0.0.1", "--port", strconv.Itoa(int(config.Port)))
	if err != nil {
		return nil, fmt.Errorf("read Tilt resources for project %q: %w", projectID, err)
	}
	var list resourceList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("decode Tilt UIResources: %w", err)
	}
	if list.Kind != "List" {
		return nil, fmt.Errorf("unexpected Tilt resource kind %q", list.Kind)
	}
	result := make([]Resource, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Metadata.Name == "" {
			return nil, errors.New("Tilt resource missing name")
		}
		result = append(result, Resource{Name: item.Metadata.Name, RuntimeStatus: item.Status.RuntimeStatus, UpdateStatus: item.Status.UpdateStatus})
	}
	return result, nil
}

// Logs returns a bounded snapshot of log lines, not an unbounded stream.
func (m *Manager) Logs(ctx context.Context, projectID, resource string, tail int) ([]byte, error) {
	if tail < 1 || tail > 1000 {
		return nil, fmt.Errorf("log tail must be between 1 and 1000, got %d", tail)
	}
	if strings.HasPrefix(resource, "-") {
		return nil, errors.New("resource name cannot begin with a CLI flag")
	}
	config, err := m.activeConfig(projectID)
	if err != nil {
		return nil, err
	}
	args := []string{"logs", "--host", "127.0.0.1", "--port", strconv.Itoa(int(config.Port)), "--tail", strconv.Itoa(tail)}
	if resource != "" {
		args = append(args, resource)
	}
	data, err := m.launcher.Run(ctx, config, args...)
	if err != nil {
		return nil, fmt.Errorf("read Tilt logs for project %q: %w", projectID, err)
	}
	return data, nil
}
