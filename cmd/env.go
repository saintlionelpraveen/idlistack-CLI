package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/auth"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage environment variables for the project",
}

var envSetCmd = &cobra.Command{
	Use:   "set KEY=VALUE [KEY=VALUE...]",
	Short: "Set environment variables",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runEnvSet,
}

var envListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all environment variables",
	RunE:  runEnvList,
}

var envDeleteCmd = &cobra.Command{
	Use:   "delete KEY [KEY...]",
	Short: "Delete environment variables",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runEnvDelete,
}

func init() {
	envCmd.AddCommand(envSetCmd)
	envCmd.AddCommand(envListCmd)
	envCmd.AddCommand(envDeleteCmd)
}

func runEnvSet(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	cwd, _ := os.Getwd()
	cfg, err := config.Load(cwd)
	if err != nil {
		return err
	}

	namespace := fmt.Sprintf("idlistack-%s", cfg.Project.Name)
	secretName := fmt.Sprintf("%s-env", cfg.Project.Name)

	// Parse KEY=VALUE pairs
	literals := []string{}
	for _, arg := range args {
		if !strings.Contains(arg, "=") {
			return fmt.Errorf("invalid format: %s (expected KEY=VALUE)", arg)
		}
		literals = append(literals, fmt.Sprintf("--from-literal=%s", arg))
	}

	// Ensure namespace exists
	auth.KubectlCommand(ctx, "create", "namespace", namespace, "--dry-run=client", "-o", "yaml").
		Output()

	// Create or update the secret
	deleteArgs := []string{"delete", "secret", secretName, "-n", namespace, "--ignore-not-found"}
	auth.KubectlCommand(ctx, deleteArgs...).Run()

	createArgs := append([]string{"create", "secret", "generic", secretName, "-n", namespace}, literals...)
	createCmd := auth.KubectlCommand(ctx, createArgs...)
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("failed to set env vars: %w", err)
	}

	for _, arg := range args {
		parts := strings.SplitN(arg, "=", 2)
		ui.Success(fmt.Sprintf("Set %s", color.CyanString(parts[0])))
	}

	ui.Info("Redeploy with 'idlistack up' to apply changes")
	return nil
}

func runEnvList(cmd *cobra.Command, args []string) error {
	cwd, _ := os.Getwd()
	cfg, err := config.Load(cwd)
	if err != nil {
		return err
	}

	namespace := fmt.Sprintf("idlistack-%s", cfg.Project.Name)
	secretName := fmt.Sprintf("%s-env", cfg.Project.Name)

	getCmd := auth.KubectlCommand(cmd.Context(), "get", "secret", secretName,
		"-n", namespace, "-o", "jsonpath={.data}")
	output, err := getCmd.Output()
	if err != nil {
		ui.Info("No environment variables set")
		return nil
	}

	fmt.Printf("  Environment variables for %s:\n\n", color.CyanString(cfg.Project.Name))

	// Parse and display (values are base64 encoded in secrets)
	data := strings.TrimSpace(string(output))
	if data == "" || data == "map[]" {
		ui.Info("No environment variables set")
		return nil
	}

	// Use kubectl to get decoded values
	descCmd := auth.KubectlCommand(cmd.Context(), "get", "secret", secretName,
		"-n", namespace, "-o", "go-template={{range $k,$v := .data}}{{$k}}={{$v | base64decode}}\n{{end}}")
	descCmd.Stdout = os.Stdout
	descCmd.Stderr = os.Stderr
	return descCmd.Run()
}

func runEnvDelete(cmd *cobra.Command, args []string) error {
	ui.Warn("To delete env vars, use 'idlistack env set' with the remaining vars, or delete the secret entirely.")
	return nil
}

