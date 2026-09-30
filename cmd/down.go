package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/auth"
	"github.com/idlistack/cli/internal/config"
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

	appName := strings.ToLower(cfg.Project.Name)
	namespace := fmt.Sprintf("idlistack-%s", appName)

	if !downForce {
		ui.Warn(fmt.Sprintf("This will delete ALL resources in namespace %s", color.RedString(namespace)))
		fmt.Print("\nType the project name to confirm: ")
		var confirmation string
		fmt.Scanln(&confirmation)
		if confirmation != cfg.Project.Name {
			ui.Error("Confirmation failed. Aborting.")
			return fmt.Errorf("user cancelled")
		}
	}

	ui.Step(1, 2, "Uninstalling Helm release")

	if err := uninstallHelmRelease(ctx, appName, namespace); err != nil {
		ui.Warn(fmt.Sprintf("Helm uninstall failed (might not exist): %v", err))
	}
	
	// Helm might not delete the namespace if it wasn't the sole creator, so let's delete it explicitly
	if err := deleteNamespace(ctx, namespace); err != nil {
		ui.Warn(fmt.Sprintf("Failed to delete namespace: %v", err))
	}

	ui.Step(2, 2, "Cleaning up local state")

	// Clean deploy lock if exists
	lockPath := fmt.Sprintf("/tmp/idlistack-%s.lock", appName)
	os.Remove(lockPath)

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
	cmd := auth.KubectlCommand(ctx, "delete", "namespace", namespace, "--ignore-not-found")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

