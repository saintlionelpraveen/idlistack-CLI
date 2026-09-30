package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/dashboard"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var (
	statusCliOnly   bool
	statusNoBrowser bool
	statusPort      int
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the current deployment status and launch interactive web dashboard",
	Long: `Displays the application's K3s deployment status, pods, and service URL.

By default, launches an interactive web dashboard in your default browser where you
can view live status, stream logs, restart, and stop/start your application.

Use --cli to only print status to the terminal without starting the web dashboard.
Use --no-browser to start the web dashboard without automatically opening the browser.`,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().BoolVarP(&statusCliOnly, "cli", "c", false, "Show terminal output only without starting web dashboard")
	statusCmd.Flags().BoolVar(&statusNoBrowser, "no-browser", false, "Start web dashboard without auto-opening browser")
	statusCmd.Flags().IntVarP(&statusPort, "port", "p", 4200, "Port for web dashboard server")
}

func runStatus(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		ui.Error("No idlistack.toml found.")
		return err
	}

	name := strings.ToLower(cfg.Project.Name)
	namespace := fmt.Sprintf("idlistack-%s", name)

	ui.PrintBanner()
	fmt.Printf("  Project:   %s\n", color.CyanString(name))
	fmt.Printf("  Namespace: %s\n\n", namespace)

	deployCmd := exec.CommandContext(cmd.Context(), "kubectl", "get", "deployment", name, "-n", namespace, "-o", "json")
	out, err := deployCmd.Output()
	if err != nil {
		ui.Warn("No active deployment found")
		return nil
	}

	var deploy map[string]interface{}
	json.Unmarshal(out, &deploy)
	st := deploy["status"].(map[string]interface{})
	sp := deploy["spec"].(map[string]interface{})
	ready := 0
	if r, ok := st["readyReplicas"]; ok {
		ready = int(r.(float64))
	}
	desired := int(sp["replicas"].(float64))

	statusText := color.GreenString("● Running")
	if ready == 0 {
		statusText = color.RedString("○ Down")
	} else if ready < desired {
		statusText = color.YellowString("◐ Deploying")
	}
	fmt.Printf("  Status: %s (%d/%d)\n", statusText, ready, desired)

	// Get URL via K3s Node IP and Service NodePort
	var appURL string
	portCmd := exec.CommandContext(cmd.Context(), "kubectl", "get", "svc", name, "-n", namespace, "-o", `jsonpath={.spec.ports[0].nodePort}`)
	if portOut, err := portCmd.Output(); err == nil {
		nodePort := strings.TrimSpace(string(portOut))
		ipCmd := exec.CommandContext(cmd.Context(), "kubectl", "get", "nodes", "-o", `jsonpath={.items[0].status.addresses[?(@.type=="InternalIP")].address}`)
		nodeIp := "127.0.0.1"
		if ipOut, err := ipCmd.Output(); err == nil && len(strings.TrimSpace(string(ipOut))) > 0 {
			nodeIp = strings.Fields(strings.TrimSpace(string(ipOut)))[0]
		}
		if nodePort != "" {
			appURL = fmt.Sprintf("http://%s:%s", nodeIp, nodePort)
			fmt.Printf("  URL:    %s\n", color.CyanString(appURL))
		}
	}

	fmt.Println()
	podsCmd := exec.CommandContext(cmd.Context(), "kubectl", "get", "pods", "-n", namespace, "-l", fmt.Sprintf("app=%s", name))
	podsCmd.Stdout = os.Stdout
	podsCmd.Stderr = os.Stderr
	podsCmd.Run()

	// If --cli flag is passed, exit cleanly here
	if statusCliOnly {
		return nil
	}

	// ─── Launch Interactive Web Dashboard ────────────────────────────────
	fmt.Println()
	dashServer, err := dashboard.NewServer(cfg, cwd, statusPort)
	if err != nil {
		ui.Warn(fmt.Sprintf("Could not initialize dashboard server: %v", err))
		return nil
	}

	if err := dashServer.Start(); err != nil {
		ui.Warn(fmt.Sprintf("Could not start dashboard server: %v", err))
		return nil
	}

	dashURL := dashServer.URL()
	ui.PrintDivider()
	fmt.Println()
	color.New(color.FgHiGreen, color.Bold).Printf("🚀 Web Dashboard: %s\n", dashURL)
	if !statusNoBrowser {
		ui.Detail("Opening web dashboard in your browser...")
		if err := dashServer.OpenBrowser(); err != nil {
			ui.Detail("Could not open browser automatically. Please open the URL manually.")
		}
	}
	ui.Info("Press Ctrl+C to close dashboard server\n")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nStopping dashboard server...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = dashServer.Stop(shutdownCtx)

	return nil
}
