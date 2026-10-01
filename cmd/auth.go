package cmd

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/auth"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/k8s"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var (
	loginCliOnly     bool
	loginUsername    string
	loginPassword    string
	loginKeycloakURL string
	loginNamespace   string
	loginPort        int
	loginStayOpen    bool
	setupK3sOIDC       bool
	setupNamespace     string
	setupRealm         string
	setupClientID      string
	setupImage         string
	setupNodePort      int
	setupHTTPPort      int
	setupAdminUser     string
	setupAdminPassword string

	newUserPassword  string
	newUserEmail     string
	newUserRole      string
	newUserNamespace string
	newUserAutoLogin bool
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with Keycloak in K3s and mint a Cluster/Namespace RBAC token",
	Long: `Authenticates with the Keycloak identity provider running inside your local K3s
cluster (namespace: idlistack-auth) and binds your session to K3s RBAC:
  - Cluster-Wide RBAC (group: cluster-admins -> ClusterRoleBinding)
  - Namespace-Wise RBAC (group: developers   -> RoleBinding in idlistack-<app>)

By default, launches the IdliStack Auth Portal web frontend in your browser.
Use --cli (or --username / --password) to authenticate directly in the terminal.`,
	RunE: runLogin,
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out and clear saved Keycloak & K3s RBAC credentials",
	RunE:  runLogout,
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Display the currently authenticated Keycloak user and K3s RBAC scope",
	RunE:  runWhoami,
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage the K3s Keycloak authentication layer and RBAC bindings",
}

var authSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Deploy Keycloak inside local K3s with dynamic realm & RBAC configuration",
	RunE:  runAuthSetup,
}

var authPortalCmd = &cobra.Command{
	Use:   "portal",
	Short: "Run the IdliStack Keycloak Auth Web Frontend server",
	RunE:  runPortalDaemon,
}

var authCreateUserCmd = &cobra.Command{
	Use:   "create-user <username>",
	Short: "Dynamically create a Keycloak user and bind their K3s RBAC permissions",
	Long: `Dynamically creates a new user (or updates an existing user) in the K3s Keycloak
realm (idlistack) and automatically provisions their K3s RBAC bindings:
  - --role developer (default): Namespace-Scoped RoleBinding in idlistack-<project>
  - --role admin:               Cluster-Wide ClusterRoleBinding (cluster-admin)`,
	Args: cobra.ExactArgs(1),
	RunE: runAuthCreateUser,
}

func init() {
	loginCmd.Flags().BoolVar(&loginCliOnly, "cli", false, "Authenticate via terminal prompt instead of browser frontend")
	loginCmd.Flags().StringVarP(&loginUsername, "username", "u", "", "Keycloak username")
	loginCmd.Flags().StringVarP(&loginPassword, "password", "p", "", "Keycloak password")
	loginCmd.Flags().StringVar(&loginKeycloakURL, "keycloak-url", "", "Keycloak base URL (auto-detected from K3s if empty)")
	loginCmd.Flags().StringVarP(&loginNamespace, "namespace", "n", "", "Target namespace scope for namespace-wise RBAC (e.g. idlistack-myapp)")
	loginCmd.Flags().IntVar(&loginPort, "port", 4201, "Port for the local authentication web frontend")
	loginCmd.Flags().BoolVar(&loginStayOpen, "stay", false, "Keep foreground process open after login")

	authPortalCmd.Flags().IntVar(&loginPort, "port", 4201, "Port for the local authentication web frontend")

	authSetupCmd.Flags().BoolVar(&setupK3sOIDC, "configure-k3s-oidc", false, "Also write OIDC issuer flags to /etc/rancher/k3s/config.yaml and restart K3s")
	authSetupCmd.Flags().StringVar(&setupNamespace, "namespace", "", "K3s namespace for Keycloak (default: idlistack-auth or $IDLISTACK_KEYCLOAK_NAMESPACE)")
	authSetupCmd.Flags().StringVar(&setupRealm, "realm", "", "Keycloak realm name (default: idlistack or $IDLISTACK_KEYCLOAK_REALM)")
	authSetupCmd.Flags().StringVar(&setupClientID, "client-id", "", "Keycloak OIDC client ID (default: idlistack-cli or $IDLISTACK_KEYCLOAK_CLIENT_ID)")
	authSetupCmd.Flags().StringVar(&setupImage, "image", "", "Keycloak container image (default: quay.io/keycloak/keycloak:24.0 or $IDLISTACK_KEYCLOAK_IMAGE)")
	authSetupCmd.Flags().IntVar(&setupNodePort, "node-port", 0, "Keycloak Service NodePort (default: 30080 or $IDLISTACK_KEYCLOAK_NODEPORT)")
	authSetupCmd.Flags().IntVar(&setupHTTPPort, "http-port", 0, "Keycloak container HTTP port (default: 8080 or $IDLISTACK_KEYCLOAK_HTTP_PORT)")
	authSetupCmd.Flags().StringVar(&setupAdminUser, "admin-user", "", "Keycloak master admin username (stored in K8s Secret)")
	authSetupCmd.Flags().StringVar(&setupAdminPassword, "admin-password", "", "Keycloak master admin password (auto-generated in K8s Secret if omitted)")

	authCreateUserCmd.Flags().StringVarP(&newUserPassword, "password", "p", "", "Password for the new user (required)")
	authCreateUserCmd.Flags().StringVarP(&newUserEmail, "email", "e", "", "Email address for the new user")
	authCreateUserCmd.Flags().StringVarP(&newUserRole, "role", "r", "developer", "RBAC role: 'developer' (Namespace-Scoped) or 'admin' (Cluster-Wide)")
	authCreateUserCmd.Flags().StringVarP(&newUserNamespace, "namespace", "n", "", "Target K3s namespace (defaults to current project idlistack-<project>)")
	authCreateUserCmd.Flags().BoolVar(&newUserAutoLogin, "login", true, "Automatically sign in as the newly created user")

	authCmd.AddCommand(authSetupCmd)
	authCmd.AddCommand(authPortalCmd)
	authCmd.AddCommand(authCreateUserCmd)
}

func runAuthCreateUser(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	username := strings.TrimSpace(args[0])
	if newUserPassword == "" {
		reader := bufio.NewReader(os.Stdin)
		fmt.Printf("  Enter password for %s: ", color.CyanString(username))
		p, _ := reader.ReadString('\n')
		newUserPassword = strings.TrimSpace(p)
	}
	if newUserPassword == "" {
		return fmt.Errorf("password is required (--password / -p)")
	}

	targetNS := newUserNamespace
	if targetNS == "" && !strings.EqualFold(newUserRole, "admin") && !strings.EqualFold(newUserRole, "cluster-wide") {
		cwd, _ := os.Getwd()
		proj := filepath.Base(cwd)
		if cfg, err := config.Load(cwd); err == nil && cfg.Project.Name != "" {
			proj = cfg.Project.Name
		}
		targetNS = "idlistack-" + strings.ToLower(proj)
	}

	ui.PrintBanner()
	ui.Info(fmt.Sprintf("Dynamically provisioning user %s in K3s Keycloak...", color.CyanString(username)))

	if newUserAutoLogin {
		creds, err := auth.RegisterAndLoginKeycloakUser(ctx, username, newUserEmail, newUserPassword, newUserRole, targetNS)
		if err != nil {
			return err
		}
		fmt.Println()
		ui.Success(fmt.Sprintf("Created user %s and provisioned K3s RBAC!", color.CyanString(username)))
		printAuthSuccess(creds)
		return nil
	}

	if err := auth.CreateKeycloakUser(ctx, username, newUserEmail, newUserPassword, newUserRole, targetNS); err != nil {
		return err
	}
	fmt.Println()
	ui.Success(fmt.Sprintf("Created user %s in Keycloak and provisioned K3s RBAC!", color.CyanString(username)))
	if strings.EqualFold(newUserRole, "admin") || strings.EqualFold(newUserRole, "cluster-wide") {
		ui.Detail("RBAC Scope: %s (group: cluster-admins -> ClusterRoleBinding)", color.GreenString("CLUSTER-WIDE"))
	} else {
		ui.Detail("RBAC Scope: %s (group: ns:%s -> RoleBinding in %s)", color.CyanString("NAMESPACE-SCOPED"), targetNS, targetNS)
	}
	return nil
}

func runAuthSetup(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	cfg := k8s.DefaultKeycloakDeployConfig(ctx)
	if setupNamespace != "" {
		cfg.Namespace = setupNamespace
	}
	if setupRealm != "" {
		cfg.Realm = setupRealm
	}
	if setupClientID != "" {
		cfg.ClientID = setupClientID
	}
	if setupImage != "" {
		cfg.Image = setupImage
	}
	if setupNodePort > 0 {
		cfg.NodePort = setupNodePort
	}
	if setupHTTPPort > 0 {
		cfg.HTTPPort = setupHTTPPort
	}
	if setupAdminUser != "" {
		cfg.AdminUser = setupAdminUser
	}
	if setupAdminPassword != "" {
		cfg.AdminPassword = setupAdminPassword
	}

	ui.PrintBanner()
	ui.Step(1, 2, fmt.Sprintf("Deploying Keycloak inside K3s cluster (namespace: %s)", cfg.Namespace))

	kcURL, err := k8s.DeployKeycloakWithConfig(ctx, cfg)
	if err != nil {
		return err
	}

	if setupK3sOIDC {
		ui.Step(2, 2, "Configuring K3s API server OIDC flags")
		if err := k8s.ConfigureK3sOIDC(ctx); err != nil {
			ui.Warn(fmt.Sprintf("Could not update /etc/rancher/k3s/config.yaml automatically: %v", err))
		} else {
			ui.Success("K3s API server configured with Keycloak OIDC issuer")
		}
	} else {
		ui.Step(2, 2, "Verifying Keycloak Realm & OIDC Client")
	}

	fmt.Println()
	ui.Success(fmt.Sprintf("Keycloak is running in K3s at %s", color.CyanString(kcURL)))
	ui.Detail("Namespace:          %s", color.CyanString(cfg.Namespace))
	ui.Detail("Realm:              %s", color.CyanString(cfg.Realm))
	ui.Detail("OIDC Client ID:     %s", color.CyanString(cfg.ClientID))
	ui.Detail("Admin Secret:       %s (in namespace %s)", color.CyanString(k8s.KeycloakAdminSecret), cfg.Namespace)
	fmt.Println()
	ui.Info(fmt.Sprintf("Next: run %s (or %s) to dynamically create a user and authenticate",
		color.CyanString("idlistack login"),
		color.CyanString("idlistack auth create-user <username> --role admin|developer")))
	return nil
}

// runPortalDaemon runs the Auth Portal HTTP server persistently so browser refreshes never fail
func runPortalDaemon(cmd *cobra.Command, args []string) error {
	cwd, _ := os.Getwd()
	defaultProject := filepath.Base(cwd)
	if cfg, err := config.Load(cwd); err == nil && cfg.Project.Name != "" {
		defaultProject = cfg.Project.Name
	}

	portal, err := auth.NewPortalServer(loginPort, defaultProject)
	if err != nil {
		return err
	}
	portal.Start()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return portal.Stop(shutdownCtx)
}

func ensureBackgroundPortalRunning(port int, defaultProject string) string {
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 800 * time.Millisecond}
	if resp, err := client.Get(baseURL + "/api/auth/status?project=" + url.QueryEscape(defaultProject)); err == nil {
		resp.Body.Close()
		return baseURL
	}

	// Spawn a persistent background `idlistack auth portal` daemon so the browser page stays alive
	exe, err := os.Executable()
	if err == nil {
		bgCmd := exec.Command(exe, "auth", "portal", "--port", fmt.Sprintf("%d", port))
		bgCmd.Stdout = nil
		bgCmd.Stderr = nil
		bgCmd.Stdin = nil
		if err := bgCmd.Start(); err == nil {
			_ = bgCmd.Process.Release()
			for i := 0; i < 15; i++ {
				time.Sleep(100 * time.Millisecond)
				if resp, err := client.Get(baseURL + "/api/auth/status?project=" + url.QueryEscape(defaultProject)); err == nil {
					resp.Body.Close()
					return baseURL
				}
			}
		}
	}
	return baseURL
}

func runLogin(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	ui.PrintBanner()

	cwd, _ := os.Getwd()
	defaultProject := filepath.Base(cwd)
	if cfg, err := config.Load(cwd); err == nil && cfg.Project.Name != "" {
		defaultProject = cfg.Project.Name
	}

	// Ensure Keycloak is deployed in K3s before logging in
	kcRunning, kcURL := k8s.IsKeycloakRunning(ctx)
	if !kcRunning && loginKeycloakURL == "" {
		ui.Warn("Keycloak is not yet running in K3s namespace 'idlistack-auth'. Deploying it now...")
		deployedURL, err := k8s.DeployKeycloakToK3s(ctx)
		if err != nil {
			ui.Warn(fmt.Sprintf("Automatic Keycloak deployment notice: %v", err))
		} else {
			kcURL = deployedURL
			ui.Success(fmt.Sprintf("Keycloak deployed in K3s at %s", color.CyanString(kcURL)))
		}
	}

	if loginKeycloakURL != "" {
		kcURL = loginKeycloakURL
	}

	// Non-interactive or terminal CLI mode
	if loginCliOnly || (loginUsername != "" && loginPassword != "") {
		reader := bufio.NewReader(os.Stdin)
		if loginUsername == "" {
			fmt.Printf("  Keycloak Username (e.g. admin / developer): ")
			u, _ := reader.ReadString('\n')
			loginUsername = strings.TrimSpace(u)
		}
		if loginPassword == "" {
			fmt.Printf("  Keycloak Password: ")
			p, _ := reader.ReadString('\n')
			loginPassword = strings.TrimSpace(p)
		}

		var allowedNS []string
		if loginNamespace != "" {
			allowedNS = []string{loginNamespace}
		} else if defaultProject != "" {
			allowedNS = []string{fmt.Sprintf("idlistack-%s", strings.ToLower(defaultProject))}
		}

		ui.Info(fmt.Sprintf("Authenticating %s with Keycloak at %s ...", color.CyanString(loginUsername), kcURL))
		creds, err := auth.LoginWithKeycloak(ctx, kcURL, k8s.KeycloakRealm, k8s.KeycloakClientID, loginUsername, loginPassword, allowedNS)
		if err != nil {
			return err
		}

		printAuthSuccess(creds)
		return nil
	}

	// Ensure persistent background Auth Portal is running on http://127.0.0.1:4201
	startTime := time.Now()
	portalURL := ensureBackgroundPortalRunning(loginPort, defaultProject)
	loginBrowserURL := fmt.Sprintf("%s/?login=1&project=%s", portalURL, url.QueryEscape(defaultProject))

	ui.PrintDivider()
	fmt.Println()
	color.New(color.FgHiCyan, color.Bold).Printf("  🔐 IdliStack Auth Frontend: %s\n", portalURL)
	ui.Detail("Keycloak Cluster URL: %s (Realm: %s)", kcURL, k8s.KeycloakRealm)
	ui.Detail("Opening authentication frontend in your browser...")
	_ = auth.OpenBrowserURL(loginBrowserURL)
	fmt.Println()
	ui.Info("Waiting for authentication in browser (or press Ctrl+C to cancel)...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if creds, err := auth.LoadCredentials(); err == nil && creds != nil {
				if creds.IssuedAt.After(startTime) {
					fmt.Println()
					printAuthSuccess(creds)
					return nil
				}
			}
		case <-sigChan:
			fmt.Println("\nCancelled.")
			return fmt.Errorf("login cancelled")
		}
	}
}

func runLogout(cmd *cobra.Command, args []string) error {
	if err := auth.ClearCredentials(); err != nil {
		return err
	}
	ui.Success("Logged out and removed ~/.idlistack/credentials.json")
	return nil
}

func runWhoami(cmd *cobra.Command, args []string) error {
	creds, err := auth.LoadCredentials()
	if err != nil {
		ui.Warn("Not logged in. Run 'idlistack login' to authenticate with Keycloak.")
		return nil
	}

	if valErr := auth.ValidateKeycloakSession(cmd.Context(), creds); valErr != nil {
		ui.Warn(fmt.Sprintf("Session Invalid: %v", valErr))
		return nil
	}

	ui.PrintBanner()
	fmt.Printf("  User:         %s\n", color.CyanString(creds.Username))
	if creds.Email != "" {
		fmt.Printf("  Email:        %s\n", creds.Email)
	}
	fmt.Printf("  RBAC Scope:   %s\n", color.GreenString(strings.ToUpper(creds.Scope)))
	fmt.Printf("  Groups:       %s\n", strings.Join(creds.Groups, ", "))
	if len(creds.AllowedNamespaces) > 0 {
		fmt.Printf("  Namespaces:   %s\n", strings.Join(creds.AllowedNamespaces, ", "))
	}
	fmt.Printf("  Keycloak URL: %s (realm: %s)\n", creds.KeycloakURL, creds.Realm)
	fmt.Printf("  Expires At:   %s\n", creds.ExpiresAt.Format(time.RFC1123))
	return nil
}

func printAuthSuccess(creds *auth.Credentials) {
	ui.Success(fmt.Sprintf("Authenticated as %s via Keycloak!", color.CyanString(creds.Username)))
	ui.Detail("RBAC Scope: %s", color.GreenString(strings.ToUpper(creds.Scope)))
	ui.Detail("Groups:     %s", strings.Join(creds.Groups, ", "))
	if creds.Scope == "namespace-scoped" && len(creds.AllowedNamespaces) > 0 {
		ui.Detail("Allowed NS: %s", strings.Join(creds.AllowedNamespaces, ", "))
	}
	ui.Detail("Token saved to ~/.idlistack/credentials.json")
}
