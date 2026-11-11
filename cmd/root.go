package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/auth"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/k8s"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var (
	// Version is set at build time
	Version = "dev"
	// Verbose enables debug output
	Verbose bool
)

var rootCmd = &cobra.Command{
	Use:   "idlistack",
	Short: "IdliStack — Deploy applications with zero configuration",
	Long: fmt.Sprintf(`%s

IdliStack is a production-ready deployment CLI that automatically detects
your application's language, framework, and dependencies — then builds and
deploys it to K3s with zero configuration.

Powered by Railpack for intelligent detection and BuildKit for optimized
OCI image builds.`, color.New(color.FgHiCyan, color.Bold).Sprint("🚀 IdliStack")),
	SilenceUsage:      true,
	SilenceErrors:     true,
	PersistentPreRunE: enforceFrappeAuthMiddleware,
}

// enforceFrappeAuthMiddleware verifies that the user has a valid Frappe session
// and active Teleport namespace-scoped RBAC permissions before running
// init, up, down, status, logs, or env commands.
func enforceFrappeAuthMiddleware(cmd *cobra.Command, args []string) error {
	// Proactively clean up and migrate any legacy .idlistack directory in workspace
	cwd, _ := os.Getwd()
	if cwd != "" {
		_, _ = config.LoadProjectState(cwd)
	}

	name := cmd.Name()
	// Exempt login, logout, whoami, auth, version, admin, init, and help
	switch name {
	case "idlistack", "login", "logout", "whoami", "auth", "admin", "version", "help", "init":
		return nil
	}
	if cmd.Parent() != nil && cmd.Parent().Name() == "auth" {
		return nil
	}

	// Exempt external kubeconfigs and remote clusters from local Frappe auth gate
	if os.Getenv("KUBECONFIG") != "" || k8s.IsClusterRemote(cmd.Context()) {
		return nil
	}

	// Determine target namespace for RBAC verification
	projectName := filepath.Base(cwd)
	targetNamespace := ""
	if cfg, err := config.Load(cwd); err == nil {
		if cfg.Project.Name != "" {
			projectName = cfg.Project.Name
		}
		if cfg.Deploy.Namespace != "" {
			targetNamespace = cfg.Deploy.Namespace
		}
	}
	targetNamespace = k8s.ResolveNamespace(cmd.Context(), targetNamespace, projectName)

	creds, err := auth.VerifyAndAuthorize(cmd.Context(), targetNamespace)
	if err != nil {
		ui.Warn(fmt.Sprintf("Frappe Auth Gate: %v", err))
		ui.Info("Launching Frappe authentication portal before executing command...")
		if loginErr := runLogin(cmd, args); loginErr != nil {
			return fmt.Errorf("authentication required to run 'idlistack %s': %w", name, loginErr)
		}
		// Re-verify after login
		creds, err = auth.VerifyAndAuthorize(cmd.Context(), targetNamespace)
		if err != nil {
			return err
		}
	}

	if Verbose && creds != nil {
		ui.Detail("Authenticated via Frappe as %s (Target Namespace: %s)", color.CyanString(creds.User), color.GreenString(targetNamespace))
	}
	return nil
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&Verbose, "verbose", "v", false, "Enable verbose output")

	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(whoamiCmd)
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(upCmd)
	rootCmd.AddCommand(downCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(envCmd)
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the IdliStack CLI version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("idlistack version %s\n", Version)
	},
}

