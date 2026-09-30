package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/auth"
	"github.com/idlistack/cli/internal/config"
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
	PersistentPreRunE: enforceKeycloakAuthMiddleware,
}

// enforceKeycloakAuthMiddleware verifies that the user has a valid Keycloak OIDC token
// and appropriate K3s RBAC permissions (cluster-wide or namespace-scoped) before running
// init, up, down, status, logs, or env commands.
func enforceKeycloakAuthMiddleware(cmd *cobra.Command, args []string) error {
	name := cmd.Name()
	// Exempt login, logout, whoami, auth setup/portal, version, and help
	switch name {
	case "idlistack", "login", "logout", "whoami", "auth", "setup", "portal", "version", "help":
		return nil
	}
	if cmd.Parent() != nil && cmd.Parent().Name() == "auth" {
		return nil
	}

	// Determine target namespace for RBAC verification
	cwd, _ := os.Getwd()
	projectName := filepath.Base(cwd)
	if cfg, err := config.Load(cwd); err == nil && cfg.Project.Name != "" {
		projectName = cfg.Project.Name
	}
	if name == "init" && initProjectName != "" {
		projectName = initProjectName
	}
	targetNamespace := fmt.Sprintf("idlistack-%s", strings.ToLower(projectName))

	creds, err := auth.VerifyAndAuthorize(cmd.Context(), targetNamespace)
	if err != nil {
		ui.Warn(fmt.Sprintf("Keycloak Auth Gate: %v", err))
		ui.Info("Launching Keycloak authentication flow before executing command...")
		if loginErr := runLogin(cmd, args); loginErr != nil {
			return fmt.Errorf("authentication required to run 'idlistack %s': %w", name, loginErr)
		}
		// Re-verify RBAC after login
		creds, err = auth.VerifyAndAuthorize(cmd.Context(), targetNamespace)
		if err != nil {
			return err
		}
	}

	if Verbose && creds != nil {
		ui.Detail("Authenticated via Keycloak as %s (RBAC: %s)", color.CyanString(creds.Username), color.GreenString(creds.Scope))
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

