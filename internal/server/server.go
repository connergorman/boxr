package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/gruejay/container-runtime/pkg/container"
	"github.com/gruejay/container-runtime/pkg/reexec"
)

type RuntimeService struct {
	runtimeapi.UnimplementedRuntimeServiceServer
	store *container.Store
}

func (r *RuntimeService) Version(_ context.Context, _ *runtimeapi.VersionRequest) (*runtimeapi.VersionResponse, error) {
	return &runtimeapi.VersionResponse{
		Version:           "v1",
		RuntimeName:       "boxr",
		RuntimeVersion:    "0.0.1",
		RuntimeApiVersion: "v1",
	}, nil
}

func (r *RuntimeService) RunPodSandbox(_ context.Context, req *runtimeapi.RunPodSandboxRequest) (*runtimeapi.RunPodSandboxResponse, error) {
	if req.Config == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}
	meta := req.Config.GetMetadata()
	id := newID()
	cfg := &container.SandboxConfig{
		ID:          id,
		Name:        meta.GetName(),
		Namespace:   meta.GetNamespace(),
		UID:         meta.GetUid(),
		Labels:      req.Config.Labels,
		Annotations: req.Config.Annotations,
		Hostname:    req.Config.Hostname,
		Namespaces:  container.DefaultNamespaces(),
		CreatedAt:   time.Now(),
	}
	if err := r.store.CreateSandbox(cfg, &container.SandboxState{Status: container.SandboxReady}); err != nil {
		return nil, status.Errorf(codes.Internal, "create sandbox: %v", err)
	}
	slog.Info("RunPodSandbox", "id", id, "name", cfg.Name, "namespace", cfg.Namespace)
	return &runtimeapi.RunPodSandboxResponse{PodSandboxId: id}, nil
}

func (r *RuntimeService) CreateContainer(_ context.Context, req *runtimeapi.CreateContainerRequest) (*runtimeapi.CreateContainerResponse, error) {
	if req.Config == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}
	if _, err := r.store.GetSandbox(req.PodSandboxId); err != nil {
		return nil, status.Errorf(codes.NotFound, "sandbox %s not found", req.PodSandboxId)
	}

	// Until image pulling is implemented, Image.Image is treated as a local rootfs path.
	rootfs := req.Config.GetImage().GetImage()
	if rootfs == "" {
		return nil, status.Error(codes.InvalidArgument, "image (rootfs path) is required")
	}
	if _, err := os.Stat(rootfs); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "rootfs %q: %v", rootfs, err)
	}

	var env []string
	for _, kv := range req.Config.GetEnvs() {
		env = append(env, kv.Key+"="+kv.Value)
	}

	id := newID()
	cfg := &container.Config{
		ID:          id,
		Name:        req.Config.GetMetadata().GetName(),
		SandboxID:   req.PodSandboxId,
		Image:       rootfs,
		RootFS:      rootfs,
		Command:     req.Config.GetCommand(),
		Args:        req.Config.GetArgs(),
		Env:         env,
		WorkingDir:  req.Config.WorkingDir,
		Labels:      req.Config.Labels,
		Annotations: req.Config.Annotations,
		LogPath:     req.Config.LogPath,
		CreatedAt:   time.Now(),
	}
	if err := r.store.CreateContainer(cfg, &container.State{Status: container.ContainerCreated}); err != nil {
		return nil, status.Errorf(codes.Internal, "create container: %v", err)
	}
	slog.Info("CreateContainer", "id", id, "name", cfg.Name, "sandbox", req.PodSandboxId)
	return &runtimeapi.CreateContainerResponse{ContainerId: id}, nil
}

func (r *RuntimeService) StartContainer(_ context.Context, req *runtimeapi.StartContainerRequest) (*runtimeapi.StartContainerResponse, error) {
	c, err := r.store.GetContainer(req.ContainerId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "container %s not found", req.ContainerId)
	}
	if c.State.Status != container.ContainerCreated {
		return nil, status.Errorf(codes.FailedPrecondition, "container is not in CREATED state (current: %v)", c.State.Status)
	}

	sb, err := r.store.GetSandbox(c.Config.SandboxID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get sandbox: %v", err)
	}

	startedAt := time.Now()
	proc, err := reexec.Start(reexec.DefaultStorePath, req.ContainerId, sb.Config.Namespaces.Flags())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "start container process: %v", err)
	}

	if err := r.store.SaveContainerState(req.ContainerId, &container.State{
		Status:    container.ContainerRunning,
		PID:       proc.Pid,
		StartedAt: startedAt,
	}); err != nil {
		proc.Kill()
		return nil, status.Errorf(codes.Internal, "save container state: %v", err)
	}

	go func() {
		ps, _ := proc.Wait()
		exitCode := int32(-1)
		if ps != nil {
			exitCode = int32(ps.ExitCode())
		}
		_ = r.store.SaveContainerState(req.ContainerId, &container.State{
			Status:     container.ContainerExited,
			PID:        proc.Pid,
			StartedAt:  startedAt,
			FinishedAt: time.Now(),
			ExitCode:   exitCode,
		})
		slog.Info("container exited", "id", req.ContainerId, "pid", proc.Pid, "exit_code", exitCode)
	}()

	slog.Info("StartContainer", "id", req.ContainerId, "pid", proc.Pid)
	return &runtimeapi.StartContainerResponse{}, nil
}

func (r *RuntimeService) ContainerStatus(_ context.Context, req *runtimeapi.ContainerStatusRequest) (*runtimeapi.ContainerStatusResponse, error) {
	c, err := r.store.GetContainer(req.ContainerId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "container %s not found", req.ContainerId)
	}

	// Live-check: if we think it's running but the PID is gone, update state.
	if c.State.Status == container.ContainerRunning && !pidAlive(c.State.PID) {
		c.State.Status = container.ContainerExited
		c.State.ExitCode = -1
		c.State.FinishedAt = time.Now()
		_ = r.store.SaveContainerState(req.ContainerId, &c.State)
	}

	criState := runtimeapi.ContainerState_CONTAINER_UNKNOWN
	switch c.State.Status {
	case container.ContainerCreated:
		criState = runtimeapi.ContainerState_CONTAINER_CREATED
	case container.ContainerRunning:
		criState = runtimeapi.ContainerState_CONTAINER_RUNNING
	case container.ContainerExited:
		criState = runtimeapi.ContainerState_CONTAINER_EXITED
	}

	return &runtimeapi.ContainerStatusResponse{
		Status: &runtimeapi.ContainerStatus{
			Id:          c.Config.ID,
			Metadata:    &runtimeapi.ContainerMetadata{Name: c.Config.Name},
			State:       criState,
			CreatedAt:   c.Config.CreatedAt.UnixNano(),
			StartedAt:   c.State.StartedAt.UnixNano(),
			FinishedAt:  c.State.FinishedAt.UnixNano(),
			ExitCode:    c.State.ExitCode,
			Image:       &runtimeapi.ImageSpec{Image: c.Config.Image},
			ImageRef:    c.Config.Image,
			Labels:      c.Config.Labels,
			Annotations: c.Config.Annotations,
		},
	}, nil
}

func Serve() {
	store, err := container.NewStore(reexec.DefaultStorePath)
	if err != nil {
		log.Fatalf("failed to create store: %v", err)
	}
	if err := store.Reconcile(); err != nil {
		slog.Warn("store reconcile on startup", "err", err)
	}

	socketPath := "/tmp/boxr.sock"
	os.Remove(socketPath)
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o666); err != nil {
		log.Fatalf("chmod socket: %v", err)
	}

	svc := &RuntimeService{store: store}
	grpcServer := grpc.NewServer()
	reflection.Register(grpcServer)
	runtimeapi.RegisterRuntimeServiceServer(grpcServer, svc)

	slog.Info("boxr CRI server listening", "socket", socketPath)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("grpc serve: %v", err)
	}
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
