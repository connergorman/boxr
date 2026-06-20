package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

const socketPath = "unix:///tmp/boxr.sock"

// rootfs returns the rootfs path to use, overridable via BOXR_ROOTFS.
func rootfs() string {
	if v := os.Getenv("BOXR_ROOTFS"); v != "" {
		return v
	}
	return "/home/ubuntu/boxr/rootfs"
}

func client(t *testing.T) runtimeapi.RuntimeServiceClient {
	t.Helper()
	if _, err := os.Stat("/tmp/boxr.sock"); err != nil {
		t.Skip("boxr socket not found — run 'sudo boxr serve' first")
	}
	conn, err := grpc.NewClient(socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial %s: %v", socketPath, err)
	}
	t.Cleanup(func() { conn.Close() })
	return runtimeapi.NewRuntimeServiceClient(conn)
}

func TestVersion(t *testing.T) {
	resp, err := client(t).Version(context.Background(), &runtimeapi.VersionRequest{})
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if resp.RuntimeName != "boxr" {
		t.Errorf("RuntimeName = %q, want %q", resp.RuntimeName, "boxr")
	}
	if resp.RuntimeVersion == "" {
		t.Error("RuntimeVersion is empty")
	}
}

func TestContainerLifecycle(t *testing.T) {
	c := client(t)
	ctx := context.Background()

	// RunPodSandbox
	sbResp, err := c.RunPodSandbox(ctx, &runtimeapi.RunPodSandboxRequest{
		Config: &runtimeapi.PodSandboxConfig{
			Metadata: &runtimeapi.PodSandboxMetadata{
				Name:      "test-pod",
				Namespace: "default",
				Uid:       "test-uid-001",
			},
		},
	})
	if err != nil {
		t.Fatalf("RunPodSandbox: %v", err)
	}
	sandboxID := sbResp.PodSandboxId
	t.Logf("sandbox: %s", sandboxID)

	// CreateContainer
	ctrResp, err := c.CreateContainer(ctx, &runtimeapi.CreateContainerRequest{
		PodSandboxId: sandboxID,
		Config: &runtimeapi.ContainerConfig{
			Metadata: &runtimeapi.ContainerMetadata{Name: "test-ctr"},
			Image:    &runtimeapi.ImageSpec{Image: rootfs()},
			Command:  []string{"/bin/sh"},
			Args:     []string{"-c", "echo hello && sleep 2"},
		},
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	ctrID := ctrResp.ContainerId
	t.Logf("container: %s", ctrID)

	// Status → CREATED before start
	st := containerStatus(t, c, ctrID)
	if got := st.State; got != runtimeapi.ContainerState_CONTAINER_CREATED {
		t.Fatalf("state before start = %v, want CREATED", got)
	}

	// StartContainer
	if _, err := c.StartContainer(ctx, &runtimeapi.StartContainerRequest{ContainerId: ctrID}); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}

	// Status → RUNNING immediately after start
	st = containerStatus(t, c, ctrID)
	if got := st.State; got != runtimeapi.ContainerState_CONTAINER_RUNNING {
		t.Errorf("state after start = %v, want RUNNING", got)
	}
	if st.StartedAt == 0 {
		t.Error("StartedAt not set")
	}

	// Wait for the container to finish (sleep 2 + a little margin)
	time.Sleep(3 * time.Second)

	// Status → EXITED with code 0
	st = containerStatus(t, c, ctrID)
	if got := st.State; got != runtimeapi.ContainerState_CONTAINER_EXITED {
		t.Errorf("state after exit = %v, want EXITED", got)
	}
	if st.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", st.ExitCode)
	}
	if st.FinishedAt == 0 {
		t.Error("FinishedAt not set")
	}
	elapsed := time.Duration(st.FinishedAt-st.StartedAt) * time.Nanosecond
	t.Logf("container ran for %v", elapsed.Round(time.Millisecond))

	// Double-start must be rejected
	t.Run("double start rejected", func(t *testing.T) {
		_, err := c.StartContainer(ctx, &runtimeapi.StartContainerRequest{ContainerId: ctrID})
		requireCode(t, err, codes.FailedPrecondition)
	})
}

func TestRunPodSandboxMissingConfig(t *testing.T) {
	_, err := client(t).RunPodSandbox(context.Background(), &runtimeapi.RunPodSandboxRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

func TestCreateContainerUnknownSandbox(t *testing.T) {
	_, err := client(t).CreateContainer(context.Background(), &runtimeapi.CreateContainerRequest{
		PodSandboxId: "doesnotexist",
		Config: &runtimeapi.ContainerConfig{
			Metadata: &runtimeapi.ContainerMetadata{Name: "x"},
			Image:    &runtimeapi.ImageSpec{Image: rootfs()},
		},
	})
	requireCode(t, err, codes.NotFound)
}

func TestCreateContainerMissingRootfs(t *testing.T) {
	c := client(t)
	ctx := context.Background()

	sbResp, err := c.RunPodSandbox(ctx, &runtimeapi.RunPodSandboxRequest{
		Config: &runtimeapi.PodSandboxConfig{
			Metadata: &runtimeapi.PodSandboxMetadata{Name: "rootfs-test", Namespace: "default", Uid: "rootfs-uid"},
		},
	})
	if err != nil {
		t.Fatalf("RunPodSandbox: %v", err)
	}

	_, err = c.CreateContainer(ctx, &runtimeapi.CreateContainerRequest{
		PodSandboxId: sbResp.PodSandboxId,
		Config: &runtimeapi.ContainerConfig{
			Metadata: &runtimeapi.ContainerMetadata{Name: "x"},
			Image:    &runtimeapi.ImageSpec{Image: "/nonexistent/rootfs"},
		},
	})
	requireCode(t, err, codes.InvalidArgument)
}

func TestStartContainerUnknown(t *testing.T) {
	_, err := client(t).StartContainer(context.Background(), &runtimeapi.StartContainerRequest{ContainerId: "doesnotexist"})
	requireCode(t, err, codes.NotFound)
}

// --- helpers ---

func containerStatus(t *testing.T, c runtimeapi.RuntimeServiceClient, id string) *runtimeapi.ContainerStatus {
	t.Helper()
	resp, err := c.ContainerStatus(context.Background(), &runtimeapi.ContainerStatusRequest{ContainerId: id})
	if err != nil {
		t.Fatalf("ContainerStatus(%s): %v", id, err)
	}
	return resp.Status
}

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %v, got nil", want)
	}
	if got := status.Code(err); got != want {
		t.Errorf("error code = %v, want %v (err: %v)", got, want, err)
	}
}
