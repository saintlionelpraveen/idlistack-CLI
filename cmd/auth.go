package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/auth"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var (
	loginCliOnly   bool
	loginUsername  string
	loginPassword  string
	loginFrappeURL string
	loginPort      int
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with Frappe backend and bind session to K3s via Teleport RBAC",
	Long: `Authenticates with your Frappe control plane (fellowship site) and binds your
developer session to K3s RBAC through Teleport:
  - Validates user identity and membership in Frappe
  - Grants Namespace-level permissions (NS level access: idlistack-<app>)
  - Mints short-lived ephemeral kubeconfigs via Teleport Machine-to-Machine

By default, opens your browser directly to the Frappe authentication portal.
Use --cli (or -u / -p) to authenticate directly inside your terminal.`,
	RunE: runLogin,
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out and revoke active Frappe CLI session",
	RunE:  runLogout,
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Display the currently authenticated Frappe user and Teleport K3s scope",
	RunE:  runWhoami,
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage Frappe authentication and Teleport K3s permissions",
}

func init() {
	loginCmd.Flags().BoolVar(&loginCliOnly, "cli", false, "Authenticate via terminal prompt instead of browser frontend")
	loginCmd.Flags().StringVarP(&loginUsername, "username", "u", "", "Frappe username / email")
	loginCmd.Flags().StringVarP(&loginPassword, "password", "p", "", "Frappe password")
	loginCmd.Flags().StringVar(&loginFrappeURL, "frappe-url", "", "Frappe base URL (default: http://localhost:8000)")
	loginCmd.Flags().IntVar(&loginPort, "port", 4201, "Port for the local authentication callback server")

	authCmd.AddCommand(loginCmd)
	authCmd.AddCommand(logoutCmd)
	authCmd.AddCommand(whoamiCmd)
}

func runLogin(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	ui.PrintBanner()

	cwd, _ := os.Getwd()
	defaultProject := filepath.Base(cwd)
	if cfg, err := config.Load(cwd); err == nil && cfg.Project.Name != "" {
		defaultProject = cfg.Project.Name
	}

	frappeURL := loginFrappeURL
	if frappeURL == "" {
		frappeURL = auth.DefaultFrappeURL()
	}

	// Non-interactive or terminal CLI mode
	if loginCliOnly || (loginUsername != "" && loginPassword != "") {
		reader := bufio.NewReader(os.Stdin)
		if loginUsername == "" {
			fmt.Printf("  Frappe Username / Email: ")
			u, _ := reader.ReadString('\n')
			loginUsername = strings.TrimSpace(u)
		}
		if loginPassword == "" {
			fmt.Printf("  Password: ")
			p, _ := reader.ReadString('\n')
			loginPassword = strings.TrimSpace(p)
		}

		if loginUsername == "" || loginPassword == "" {
			return fmt.Errorf("username and password are required")
		}

		ui.Info(fmt.Sprintf("Authenticating %s with Frappe at %s ...", color.CyanString(loginUsername), frappeURL))
		quickCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		creds, err := auth.LoginWithFrappeAPI(quickCtx, frappeURL, loginUsername, loginPassword)
		if err != nil {
			return err
		}

		printAuthSuccess(creds)
		return nil
	}

	// Browser authentication flow
	ui.PrintDivider()
	fmt.Println()
	color.New(color.FgHiCyan, color.Bold).Printf("  🔐 Frappe Auth Portal: %s\n", frappeURL)

	creds, err := auth.StartFrappeBrowserLogin(ctx, frappeURL, loginPort, defaultProject)
	if err != nil {
		return err
	}

	fmt.Println()
	printAuthSuccess(creds)
	return nil
}

func runLogout(cmd *cobra.Command, args []string) error {
	if err := auth.LogoutFrappe(cmd.Context()); err != nil {
		return err
	}
	ui.Success("Logged out of Frappe and deleted local credentials.")
	return nil
}

func runWhoami(cmd *cobra.Command, args []string) error {
	creds, err := auth.LoadCredentials()
	if err != nil {
		ui.Warn("Not logged in. Run 'idlistack login' to authenticate with Frappe.")
		return nil
	}

	if valErr := auth.ValidateFrappeSession(cmd.Context(), creds); valErr != nil {
		ui.Warn(fmt.Sprintf("Session Invalid: %v", valErr))
		return nil
	}

	ui.PrintBanner()
	fmt.Printf("  User:         %s\n", color.CyanString(creds.User))
	fmt.Printf("  Frappe URL:   %s\n", creds.FrappeURL)
	if len(creds.AllowedNamespaces) > 0 {
		fmt.Printf("  Namespaces:   %s\n", color.GreenString(strings.Join(creds.AllowedNamespaces, ", ")))
	} else {
		fmt.Printf("  Namespaces:   %s\n", color.YellowString("Dynamic (Allocated on first deploy)"))
	}
	fmt.Printf("  RBAC Scope:   %s\n", color.GreenString("Namespace-Restricted (Teleport M2M)"))
	fmt.Printf("  Expires At:   %s\n", creds.ExpiresAt.Format(time.RFC1123))
	return nil
}

func printAuthSuccess(creds *auth.Credentials) {
	ui.Success(fmt.Sprintf("Authenticated as %s via Frappe!", color.CyanString(creds.User)))
	ui.Detail("Frappe URL: %s", creds.FrappeURL)
	ui.Detail("Teleport RBAC: Namespace-Restricted (NS Level Access)")
	if len(creds.AllowedNamespaces) > 0 {
		ui.Detail("Allowed NS: %s", strings.Join(creds.AllowedNamespaces, ", "))
	}
	ui.Detail("Session token saved to ~/.idlistack/credentials.json")
}
