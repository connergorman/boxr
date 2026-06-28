package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gruejay/container-runtime/internal/server"
	"github.com/gruejay/container-runtime/pkg/reexec"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

const socketAddr = "unix:///tmp/boxr.sock"

func dial() (*grpc.ClientConn, error) {
	return grpc.NewClient(socketAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func main() {
	reexec.Init() // exits if this is a reexec'd container child
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "boxr",
	Short: "A simple container runtime",
}

var root string
var name string

func init() {
	runCmd.Flags().StringVarP(&root, "root", "r", "rootfs", "Root filesystem path")
	runCmd.Flags().StringVarP(&name, "name", "n", "", "Container name (default: random)")
	runCmd.MarkFlagRequired("root")
	rootCmd.AddCommand(runCmd, listCmd, stopCmd, killCmd, serveCmd)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

var runCmd = &cobra.Command{
	Use:   "run [command]",
	Short: "Run a command in a container",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

		conn, err := dial()
		if err != nil {
			fmt.Printf("Error connecting to server: %v\n", err)
			os.Exit(1)
		}
		defer conn.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		rt := runtimeapi.NewRuntimeServiceClient(conn)

		sbResp, err := rt.RunPodSandbox(ctx, &runtimeapi.RunPodSandboxRequest{
			Config: &runtimeapi.PodSandboxConfig{
				Metadata: &runtimeapi.PodSandboxMetadata{
					Name:      "boxr",
					Namespace: "default",
					Uid:       "boxr",
				},
			},
		})
		if err != nil {
			fmt.Printf("Error creating sandbox: %v\n", err)
			os.Exit(1)
		}

		ctrName := name
		if ctrName == "" {
			ctrName = newID()
		}

		ctrResp, err := rt.CreateContainer(ctx, &runtimeapi.CreateContainerRequest{
			PodSandboxId: sbResp.PodSandboxId,
			Config: &runtimeapi.ContainerConfig{
				Metadata: &runtimeapi.ContainerMetadata{Name: ctrName},
				Image:    &runtimeapi.ImageSpec{Image: absRoot},
				Command:  args[:1],
				Args:     args[1:],
			},
		})
		if err != nil {
			fmt.Printf("Error creating container: %v\n", err)
			os.Exit(1)
		}

		if _, err := rt.StartContainer(ctx, &runtimeapi.StartContainerRequest{
			ContainerId: ctrResp.ContainerId,
		}); err != nil {
			fmt.Printf("Error starting container: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Container %s started\n", ctrResp.ContainerId)
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List containers",
	Run: func(cmd *cobra.Command, args []string) {
		conn, err := dial()
		if err != nil {
			fmt.Printf("Error connecting to server: %v\n", err)
			os.Exit(1)
		}
		defer conn.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		resp, err := runtimeapi.NewRuntimeServiceClient(conn).ListContainers(ctx, &runtimeapi.ListContainersRequest{})
		if err != nil {
			fmt.Printf("Error listing containers: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("%-18s  %-16s  %-10s  %s\n", "ID", "NAME", "STATE", "IMAGE")
		for _, c := range resp.Containers {
			fmt.Printf("%-18s  %-16s  %-10s  %s\n",
				c.Id, c.Metadata.GetName(), criStateName(c.State), c.ImageRef)
		}
	},
}

func criStateName(s runtimeapi.ContainerState) string {
	switch s {
	case runtimeapi.ContainerState_CONTAINER_CREATED:
		return "created"
	case runtimeapi.ContainerState_CONTAINER_RUNNING:
		return "running"
	case runtimeapi.ContainerState_CONTAINER_EXITED:
		return "exited"
	default:
		return "unknown"
	}
}

var stopCmd = &cobra.Command{
	Use:   "stop [id]",
	Short: "Stop a running container",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("stop: not yet implemented")
	},
}

var killCmd = &cobra.Command{
	Use:   "kill [id]",
	Short: "Kill a running container",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("kill: not yet implemented")
	},
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the CRI gRPC server",
	Run: func(cmd *cobra.Command, args []string) {
		server.Serve()
	},
}
