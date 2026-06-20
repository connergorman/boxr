package container

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// NamespaceConfig specifies which Linux namespaces to create for a container.
type NamespaceConfig struct {
	PID     bool `json:"pid"`
	Network bool `json:"network"`
	Mount   bool `json:"mount"`
	UTS     bool `json:"uts"`
	IPC     bool `json:"ipc"`
	User    bool `json:"user"`
	Cgroup  bool `json:"cgroup"`
}

// Flags converts the config to clone(2) flags.
func (n NamespaceConfig) Flags() uintptr {
	var f uintptr
	if n.PID     { f |= syscall.CLONE_NEWPID    }
	if n.Network { f |= syscall.CLONE_NEWNET    }
	if n.Mount   { f |= syscall.CLONE_NEWNS     }
	if n.UTS     { f |= syscall.CLONE_NEWUTS    }
	if n.IPC     { f |= syscall.CLONE_NEWIPC    }
	if n.User    { f |= syscall.CLONE_NEWUSER   }
	if n.Cgroup  { f |= syscall.CLONE_NEWCGROUP }
	return f
}

// DefaultNamespaces returns a baseline isolation set.
func DefaultNamespaces() NamespaceConfig {
	return NamespaceConfig{PID: true, Mount: true, UTS: true}
}

// --- Sandbox (CRI PodSandbox) ---

type SandboxStatus int32

const (
	SandboxReady    SandboxStatus = 0
	SandboxNotReady SandboxStatus = 1
)

// SandboxConfig is written once at RunPodSandbox and never mutated.
type SandboxConfig struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	UID         string            `json:"uid"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	Hostname    string            `json:"hostname"`
	Namespaces  NamespaceConfig   `json:"namespaces"`
	CreatedAt   time.Time         `json:"created_at"`
}

type SandboxState struct {
	Status SandboxStatus `json:"status"`
	PID    int           `json:"pid"`
	NetNS  string        `json:"net_ns"`
	IP     string        `json:"ip"`
}

type Sandbox struct {
	Config SandboxConfig
	State  SandboxState
}

// --- Container ---

type ContainerStatus int32

const (
	ContainerCreated ContainerStatus = 0
	ContainerRunning ContainerStatus = 1
	ContainerExited  ContainerStatus = 2
	ContainerUnknown ContainerStatus = 3
)

// Config holds the immutable container specification written at CreateContainer.
// Command+Args follow CRI semantics: Command overrides the image entrypoint,
// Args override the image cmd.
type Config struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	SandboxID   string            `json:"sandbox_id"`
	Image       string            `json:"image"`
	RootFS      string            `json:"rootfs"`
	Command     []string          `json:"command"`
	Args        []string          `json:"args"`
	Env         []string          `json:"env"`
	WorkingDir  string            `json:"working_dir"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	LogPath     string            `json:"log_path"`
	CreatedAt   time.Time         `json:"created_at"`
}

// State is rewritten on every status transition.
type State struct {
	Status     ContainerStatus `json:"status"`
	PID        int             `json:"pid"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at"`
	ExitCode   int32           `json:"exit_code"`
	ExitReason string          `json:"exit_reason"`
}

// Container is the in-memory view loaded from disk.
type Container struct {
	Config Config
	State  State
}

// Run sets up the container filesystem and execs into the container process.
// Called inside the reexec'd child after Linux namespaces have been created.
// Never returns on success.
func (cfg *Config) Run() error {
	slog.Info("container run", "id", cfg.ID, "command", cfg.Command, "args", cfg.Args, "rootfs", cfg.RootFS)

	if err := syscall.Mount("none", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("remount root private: %w", err)
	}
	if err := syscall.Chroot(cfg.RootFS); err != nil {
		return fmt.Errorf("chroot %s: %w", cfg.RootFS, err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}
	if err := os.MkdirAll("/proc", 0o555); err != nil {
		return fmt.Errorf("mkdir /proc: %w", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		return fmt.Errorf("mount proc: %w", err)
	}

	argv := append(cfg.Command, cfg.Args...)
	binary, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("look path %s: %w", argv[0], err)
	}

	env := cfg.Env
	if len(env) == 0 {
		env = os.Environ()
	}

	return syscall.Exec(binary, argv, env)
}
