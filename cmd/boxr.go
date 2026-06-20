package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gruejay/container-runtime/internal/server"
	"github.com/gruejay/container-runtime/pkg/container"
	"github.com/gruejay/container-runtime/pkg/reexec"
	"github.com/spf13/cobra"
)

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

var (
	detach bool
	root   string
)

func init() {
	runCmd.Flags().BoolVarP(&detach, "detach", "d", false, "Run container in background")
	runCmd.Flags().StringVarP(&root, "root", "r", "rootfs", "Root filesystem path")
	runCmd.MarkFlagRequired("root")
	rootCmd.AddCommand(runCmd, stopCmd, killCmd, serveCmd)
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

		store, err := container.NewStore(reexec.DefaultStorePath)
		if err != nil {
			fmt.Printf("Error opening store: %v\n", err)
			os.Exit(1)
		}

		id := newID()
		cfg := &container.Config{
			ID:        id,
			Name:      id,
			RootFS:    absRoot,
			Command:   args[:1],
			Args:      args[1:],
			CreatedAt: time.Now(),
		}
		if err := store.CreateContainer(cfg, &container.State{Status: container.ContainerCreated}); err != nil {
			fmt.Printf("Error creating container: %v\n", err)
			os.Exit(1)
		}

		nsFlags := container.DefaultNamespaces().Flags()
		proc, err := reexec.Start(reexec.DefaultStorePath, id, nsFlags, detach)
		if err != nil {
			store.DeleteContainer(id)
			fmt.Printf("Error starting container: %v\n", err)
			os.Exit(1)
		}

		if detach {
			fmt.Printf("Container %s started (PID %d)\n", id, proc.Pid)
			return
		}

		proc.Wait()
		store.DeleteContainer(id)
	},
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

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
