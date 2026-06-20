package container

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

const (
	sandboxesDir  = "sandboxes"
	containersDir = "containers"
	configFile    = "config.json"
	stateFile     = "state.json"
)

// Store manages sandbox and container state on disk.
// All exported methods are safe for concurrent use.
type Store struct {
	base string
	mu   sync.RWMutex
}

// NewStore creates a Store rooted at base, creating the required subdirectories.
func NewStore(base string) (*Store, error) {
	for _, sub := range []string{sandboxesDir, containersDir} {
		if err := os.MkdirAll(filepath.Join(base, sub), 0o700); err != nil {
			return nil, fmt.Errorf("create store dir %s: %w", sub, err)
		}
	}
	return &Store{base: base}, nil
}

// Reconcile scans all containers and marks any that claim to be RUNNING but
// whose PID is no longer alive as EXITED. Call once at startup.
func (s *Store) Reconcile() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	containers, err := s.listContainersLocked()
	if err != nil {
		return fmt.Errorf("reconcile list: %w", err)
	}
	for _, c := range containers {
		if c.State.Status != ContainerRunning {
			continue
		}
		if c.State.PID <= 0 || !pidAlive(c.State.PID) {
			c.State.Status = ContainerExited
			c.State.ExitCode = -1
			c.State.ExitReason = "process not found on reconcile"
			if err := s.writeContainerStateLocked(c.Config.ID, &c.State); err != nil {
				return fmt.Errorf("reconcile write %s: %w", c.Config.ID, err)
			}
		}
	}
	return nil
}

// --- Sandbox ---

func (s *Store) CreateSandbox(cfg *SandboxConfig, st *SandboxState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sandboxDir(cfg.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir sandbox: %w", err)
	}
	if err := atomicWriteJSON(filepath.Join(dir, configFile), cfg); err != nil {
		return err
	}
	return atomicWriteJSON(filepath.Join(dir, stateFile), st)
}

func (s *Store) GetSandbox(id string) (*Sandbox, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getSandboxLocked(id)
}

func (s *Store) ListSandboxes() ([]*Sandbox, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listSandboxesLocked()
}

func (s *Store) SaveSandboxState(id string, st *SandboxState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicWriteJSON(filepath.Join(s.sandboxDir(id), stateFile), st)
}

func (s *Store) DeleteSandbox(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(s.sandboxDir(id))
}

// --- Container ---

func (s *Store) CreateContainer(cfg *Config, st *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.containerDir(cfg.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir container: %w", err)
	}
	if err := atomicWriteJSON(filepath.Join(dir, configFile), cfg); err != nil {
		return err
	}
	return atomicWriteJSON(filepath.Join(dir, stateFile), st)
}

func (s *Store) GetContainer(id string) (*Container, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getContainerLocked(id)
}

func (s *Store) ListContainers() ([]*Container, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listContainersLocked()
}

func (s *Store) SaveContainerState(id string, st *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeContainerStateLocked(id, st)
}

func (s *Store) DeleteContainer(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(s.containerDir(id))
}

// --- locked internals ---

func (s *Store) getSandboxLocked(id string) (*Sandbox, error) {
	dir := s.sandboxDir(id)
	var cfg SandboxConfig
	if err := readJSON(filepath.Join(dir, configFile), &cfg); err != nil {
		return nil, fmt.Errorf("sandbox %s config: %w", id, err)
	}
	var st SandboxState
	if err := readJSON(filepath.Join(dir, stateFile), &st); err != nil {
		return nil, fmt.Errorf("sandbox %s state: %w", id, err)
	}
	return &Sandbox{Config: cfg, State: st}, nil
}

func (s *Store) listSandboxesLocked() ([]*Sandbox, error) {
	entries, err := os.ReadDir(filepath.Join(s.base, sandboxesDir))
	if err != nil {
		return nil, err
	}
	var out []*Sandbox
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sb, err := s.getSandboxLocked(e.Name())
		if err != nil {
			continue
		}
		out = append(out, sb)
	}
	return out, nil
}

func (s *Store) getContainerLocked(id string) (*Container, error) {
	dir := s.containerDir(id)
	var cfg Config
	if err := readJSON(filepath.Join(dir, configFile), &cfg); err != nil {
		return nil, fmt.Errorf("container %s config: %w", id, err)
	}
	var st State
	if err := readJSON(filepath.Join(dir, stateFile), &st); err != nil {
		return nil, fmt.Errorf("container %s state: %w", id, err)
	}
	return &Container{Config: cfg, State: st}, nil
}

func (s *Store) listContainersLocked() ([]*Container, error) {
	entries, err := os.ReadDir(filepath.Join(s.base, containersDir))
	if err != nil {
		return nil, err
	}
	var out []*Container
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, err := s.getContainerLocked(e.Name())
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Store) writeContainerStateLocked(id string, st *State) error {
	return atomicWriteJSON(filepath.Join(s.containerDir(id), stateFile), st)
}

func (s *Store) sandboxDir(id string) string {
	return filepath.Join(s.base, sandboxesDir, id)
}

func (s *Store) containerDir(id string) string {
	return filepath.Join(s.base, containersDir, id)
}

func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// pidAlive returns true if the process exists. Signal 0 tests existence
// without delivering anything; EPERM means it exists but we lack permission.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
