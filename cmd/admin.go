package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/admin"
	"github.com/idlistack/cli/internal/k8s"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var (
	adminPort      int
	adminNoBrowser bool
)

var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Launch the IdliStack Admin Dashboard",
	Long: `Launches the production-ready IdliStack Admin Console in your browser.

Monitor cluster resource allocation (CPU, RAM, Pods), inspect all tenant applications
across namespaces, restart services, scale replicas, stream container logs, and manage
Keycloak tenant users.`,
	RunE: runAdmin,
}

func init() {
	adminCmd.Flags().IntVarP(&adminPort, "port", "p", 4202, "Port for the Admin Dashboard server")
	adminCmd.Flags().BoolVar(&adminNoBrowser, "no-browser", false, "Start Admin Dashboard without auto-opening the browser")
	rootCmd.AddCommand(adminCmd)
}

func runAdmin(cmd *cobra.Command, args []string) error {
	ui.PrintBanner()

	nodeIP := k8s.ResolveClusterNodeIP(cmd.Context())

	ui.Step(1, 2, "Initializing IdliStack Admin Console")
	srv, err := admin.NewServer(adminPort)
	if err != nil {
		return fmt.Errorf("failed to start admin server: %w", err)
	}

	if err := srv.Start(); err != nil {
		return fmt.Errorf("failed to bind admin server listener: %w", err)
	}

	ui.Step(2, 2, "Admin Dashboard is live")
	fmt.Println()
	boldCyan := color.New(color.FgCyan, color.Bold)
	boldGreen := color.New(color.FgGreen, color.Bold)

	fmt.Printf("  %s  %s\n", boldGreen.Sprint("Cluster:"), boldCyan.Sprintf("https://%s:6443", nodeIP))
	fmt.Printf("  %s  %s\n", boldGreen.Sprint("Dashboard URL:"), boldCyan.Sprint(srv.URL()))
	fmt.Printf("  %s  %s\n", boldGreen.Sprint("Keycloak IAM:"), boldCyan.Sprintf("http://%s:30080", nodeIP))
	fmt.Println()

	if !adminNoBrowser {
		ui.Detail("Opening Admin Console in your browser...")
		_ = srv.OpenBrowser()
	}

	ui.Info("Press Ctrl+C to stop the Admin Dashboard server\n")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nStopping Admin Dashboard server...")
	stopCtx, cancel := context.WithTimeout(context.Background(), 3)
	defer cancel()
	_ = srv.Stop(stopCtx)
	ui.Success("Admin Dashboard stopped cleanly.")

	return nil
}
