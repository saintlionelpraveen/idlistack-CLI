package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/auth"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/k8s"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Tear down the current project's deployment",
	Long: `Removes all K3s resources associated with the current project:
  - Deployments
  - Services
  - Ingresses
  - Secrets
  - ConfigMaps
  - The project namespace itself

This does NOT remove the built images from K3s containerd.`,
	RunE: runDown,
}

var (
	downForce bool
)

func init() {
	downCmd.Flags().BoolVarP(&downForce, "force", "f", false, "Skip confirmation prompt")
}

func runDown(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	cfg, err := config.Load(cwd)
	if err != nil {
		ui.Error("No idlistack.toml found. Is this an IdliStack project?")
		return err
	}

	namespace := k8s.ResolveNamespace(ctx, cfg.Deploy.Namespace, cfg.Project.Name)
	appName := k8s.CleanAppName(cfg.Project.Name)
	if appName == "" || appName == "app" {
		appName = k8s.CleanAppName(namespace)
	}

	if !downForce {
		ui.Warn(fmt.Sprintf("This will delete application %s in namespace %s", color.CyanString(appName), color.RedString(namespace)))
		fmt.Print("\nType the project name to confirm: ")
		var confirmation string
		fmt.Scanln(&confirmation)
		trimmed := strings.TrimSpace(confirmation)
		if trimmed != cfg.Project.Name && trimmed != appName && trimmed != namespace && trimmed != "yes" && trimmed != "y" {
			ui.Error("Confirmation failed. Aborting.")
			return fmt.Errorf("user cancelled")
		}
	}

	ui.Step(1, 2, "Uninstalling Helm release")

	if err := uninstallHelmRelease(ctx, appName, namespace); err != nil {
		ui.Warn(fmt.Sprintf("Helm uninstall failed (might not exist): %v", err))
	}

	// Delete app-specific resources
	_ = auth.KubectlCommand(ctx, "delete", "deployment,svc,secret,configmap", appName, "-n", namespace, "--ignore-not-found").Run()
	_ = auth.KubectlCommand(ctx, "delete", "secret", fmt.Sprintf("%s-env", appName), "-n", namespace, "--ignore-not-found").Run()

	// Only delete the namespace if it is an isolated idlistack-* namespace and cluster is local
	if strings.HasPrefix(namespace, "idlistack-") && !k8s.IsClusterRemote(ctx) {
		if err := deleteNamespace(ctx, namespace); err != nil {
			ui.Warn(fmt.Sprintf("Failed to delete namespace: %v", err))
		}
	}

	ui.Step(2, 2, "Cleaning up local state")

	// Clean deploy lock if exists
	lockPath := fmt.Sprintf("/tmp/idlistack-%s.lock", appName)
	os.Remove(lockPath)

	// Update project state in .idlistack.json
	if state, _ := config.LoadProjectState(cwd); state != nil {
		state.LastStatus = "torn_down"
		state.AccessURL = ""
		_ = config.SaveProjectState(cwd, state)
	}
	_ = os.RemoveAll(filepath.Join(cwd, ".idlistack"))

	// Report teardown to Frappe backend
	auth.RecordTeardown(ctx, appName, namespace)

	ui.Success(fmt.Sprintf("Project %s torn down successfully", color.CyanString(cfg.Project.Name)))
	return nil
}

func uninstallHelmRelease(ctx context.Context, appName, namespace string) error {
	cmd := auth.HelmCommand(ctx, "uninstall", appName, "-n", namespace, "--ignore-not-found")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func deleteNamespace(ctx context.Context, namespace string) error {
	// Pass --wait=false to avoid client-go attempting a cluster-wide watch on namespaces
	cmd := auth.KubectlCommand(ctx, "delete", "namespace", namespace, "--wait=false", "--ignore-not-found")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

