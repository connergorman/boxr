package reexec

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"github.com/gruejay/container-runtime/pkg/container"
)

const DefaultStorePath = "/var/lib/boxr"

const (
	envContainerID = "_BOXR_CONTAINER_ID"
	envStorePath   = "_BOXR_STORE"
)

// Init checks whether this process is a reexec'd container child.
// If so, loads the container config from the store and execs into it.
// Must be called before any CLI flag parsing (i.e., top of main).
//
// Previously this used cobra sub-commands to re-invoke self; it now
// uses env-var + store handoff so the child needs no CLI knowledge.
func Init() {
	id := os.Getenv(envContainerID)
	if id == "" {
		return
	}

	storePath := os.Getenv(envStorePath)
	if storePath == "" {
		storePath = DefaultStorePath
	}

	store, err := container.NewStore(storePath)
	if err != nil {
		slog.Error("container init: open store", "err", err)
		os.Exit(1)
	}

	c, err := store.GetContainer(id)
	if err != nil {
		slog.Error("container init: get container", "id", id, "err", err)
		os.Exit(1)
	}

	if err := c.Config.Run(); err != nil {
		slog.Error("container init: run", "err", err)
		os.Exit(1)
	}

	os.Exit(0)
}

// Start forks /proc/self/exe into new Linux namespaces and wires it to run
// the container identified by containerID. The child detects the env var in
// Init() and calls Run() instead of the normal CLI path.
//
// The child is always placed in a new session (Setsid) so it is detached from
// the caller's terminal and survives if the caller exits — matching how CRI
// runtimes work: spawn, record the PID, return immediately.
func Start(storePath, containerID string, nsFlags uintptr) (*os.Process, error) {
	self, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return nil, fmt.Errorf("readlink /proc/self/exe: %w", err)
	}

	cmd := exec.Command(self)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = append(os.Environ(),
		envContainerID+"="+containerID,
		envStorePath+"="+storePath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: nsFlags,
		Setsid:     true,
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start container process: %w", err)
	}

	return cmd.Process, nil
}
