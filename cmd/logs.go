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

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Stream live logs from the deployed application",
	Long: `Streams real-time logs from the application's pods in K3s.

Equivalent to 'kubectl logs -f' but automatically targets the correct
namespace and deployment for the current IdliStack project.`,
	RunE: runLogs,
}

var (
	logsTail   int
	logsFollow bool
)

func init() {
	logsCmd.Flags().IntVarP(&logsTail, "tail", "t", 100, "Number of recent log lines to show")
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", true, "Follow/stream log output")
}

func runLogs(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	cfg, err := config.Load(cwd)
	if err != nil {
		ui.Error("No idlistack.toml found. Is this an IdliStack project?")
		return err
	}

	deploymentName := strings.ToLower(cfg.Project.Name)
	namespace := fmt.Sprintf("idlistack-%s", deploymentName)

	ui.Info(fmt.Sprintf("Streaming logs for %s ...", color.CyanString(deploymentName)))
	fmt.Println()

	kubectlArgs := []string{
		"logs",
		fmt.Sprintf("deployment/%s", deploymentName),
		"-n", namespace,
		fmt.Sprintf("--tail=%d", logsTail),
	}

	if logsFollow {
		kubectlArgs = append(kubectlArgs, "-f")
	}

	kubectlCmd := auth.KubectlCommand(cmd.Context(), kubectlArgs...)
	kubectlCmd.Stdout = os.Stdout
	kubectlCmd.Stderr = os.Stderr
	kubectlCmd.Stdin = os.Stdin

	return kubectlCmd.Run()
}

