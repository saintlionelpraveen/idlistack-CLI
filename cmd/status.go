package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/k8s"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the current deployment status, pods, and service URL",
	Long:  `Displays the application's K3s deployment status, pods, and service URL directly in your terminal.`,
	RunE:  runStatus,
}

func init() {
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

	namespace := k8s.ResolveNamespace(cmd.Context(), cfg.Deploy.Namespace, cfg.Project.Name)
	name := k8s.CleanAppName(cfg.Project.Name)
	if name == "" || name == "app" {
		name = k8s.CleanAppName(namespace)
	}

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
		nodeIp := k8s.GetNodeIP(cmd.Context())
		if nodePort != "" {
			appURL = fmt.Sprintf("http://%s:%s", nodeIp, nodePort)
			fmt.Printf("  URL:    %s\n", color.CyanString(appURL))
		}
	}

	fmt.Println()
	podsCmd := exec.CommandContext(cmd.Context(), "kubectl", "get", "pods", "-n", namespace, "-l", fmt.Sprintf("app=%s", name))
	podsCmd.Stdout = os.Stdout
	podsCmd.Stderr = os.Stderr
	_ = podsCmd.Run()

	return nil
}
