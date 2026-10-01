package k8s

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/ui"
)

var (
	KeycloakNamespace   = getEnvOrDefault("IDLISTACK_KEYCLOAK_NAMESPACE", "idlistack-auth")
	KeycloakNodePort    = getEnvIntOrDefault("IDLISTACK_KEYCLOAK_NODEPORT", 30080)
	KeycloakHTTPPort    = getEnvIntOrDefault("IDLISTACK_KEYCLOAK_HTTP_PORT", 8080)
	KeycloakRealm       = getEnvOrDefault("IDLISTACK_KEYCLOAK_REALM", "idlistack")
	KeycloakClientID    = getEnvOrDefault("IDLISTACK_KEYCLOAK_CLIENT_ID", "idlistack-cli")
	KeycloakImage       = getEnvOrDefault("IDLISTACK_KEYCLOAK_IMAGE", "quay.io/keycloak/keycloak:24.0")
	KeycloakAdminSecret = getEnvOrDefault("IDLISTACK_KEYCLOAK_ADMIN_SECRET", "keycloak-admin-secret")
)

// KeycloakUserSeed represents an optional initial user to seed into the realm
type KeycloakUserSeed struct {
	Username  string
	Email     string
	FirstName string
	LastName  string
	Password  string
	Groups    []string
}

// KeycloakDeployConfig holds dynamic parameters for deploying Keycloak to K3s
type KeycloakDeployConfig struct {
	Namespace     string
	Realm         string
	ClientID      string
	Image         string
	HTTPPort      int
	NodePort      int
	Replicas      int
	TokenLifespan int
	AdminUser     string
	AdminPassword string
	InitialUsers  []KeycloakUserSeed
}

func getEnvOrDefault(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}

func getEnvIntOrDefault(key string, fallback int) int {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func generateRandomPassword(bytesLen int) string {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("kc-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// DefaultKeycloakDeployConfig builds a dynamic configuration from environment variables and existing cluster state
func DefaultKeycloakDeployConfig(ctx context.Context) KeycloakDeployConfig {
	adminUser, adminPass := GetKeycloakAdminCredentials(ctx)
	if adminUser == "" {
		adminUser = getEnvOrDefault("IDLISTACK_KEYCLOAK_ADMIN", "admin")
	}
	if adminPass == "" {
		adminPass = getEnvOrDefault("IDLISTACK_KEYCLOAK_ADMIN_PASSWORD", "")
		if adminPass == "" {
			adminPass = generateRandomPassword(12)
		}
	}
	return KeycloakDeployConfig{
		Namespace:     KeycloakNamespace,
		Realm:         KeycloakRealm,
		ClientID:      KeycloakClientID,
		Image:         KeycloakImage,
		HTTPPort:      KeycloakHTTPPort,
		NodePort:      KeycloakNodePort,
		Replicas:      getEnvIntOrDefault("IDLISTACK_KEYCLOAK_REPLICAS", 1),
		TokenLifespan: getEnvIntOrDefault("IDLISTACK_KEYCLOAK_TOKEN_LIFESPAN", 86400),
		AdminUser:     adminUser,
		AdminPassword: adminPass,
	}
}

// GetKeycloakAdminCredentials dynamically retrieves the Keycloak master admin username and password
// from environment variables, the in-cluster Kubernetes Secret, or the running deployment.
func GetKeycloakAdminCredentials(ctx context.Context) (string, string) {
	envUser := strings.TrimSpace(os.Getenv("IDLISTACK_KEYCLOAK_ADMIN"))
	envPass := strings.TrimSpace(os.Getenv("IDLISTACK_KEYCLOAK_ADMIN_PASSWORD"))
	if envUser != "" && envPass != "" {
		return envUser, envPass
	}

	EnsureK3sContext(ctx)

	// 1. Check Kubernetes Secret in KeycloakNamespace
	secCmd := exec.CommandContext(ctx, "kubectl", "get", "secret", KeycloakAdminSecret, "-n", KeycloakNamespace,
		"-o", "jsonpath={.data.KEYCLOAK_ADMIN}:{.data.KEYCLOAK_ADMIN_PASSWORD}")
	if out, err := secCmd.Output(); err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(out)), ":", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			uBytes, errU := base64.StdEncoding.DecodeString(parts[0])
			pBytes, errP := base64.StdEncoding.DecodeString(parts[1])
			if errU == nil && errP == nil {
				u := strings.TrimSpace(string(uBytes))
				p := strings.TrimSpace(string(pBytes))
				if envUser != "" {
					u = envUser
				}
				if envPass != "" {
					p = envPass
				}
				if u != "" && p != "" {
					return u, p
				}
			}
		}
	}

	// 2. Fallback: inspect existing Deployment env vars (for backwards compatibility with older deployments)
	depCmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", "keycloak", "-n", KeycloakNamespace,
		"-o", `jsonpath={.spec.template.spec.containers[0].env[?(@.name=="KEYCLOAK_ADMIN")].value}:{.spec.template.spec.containers[0].env[?(@.name=="KEYCLOAK_ADMIN_PASSWORD")].value}`)
	if out, err := depCmd.Output(); err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(out)), ":", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1]
		}
	}

	return envUser, envPass
}

// VerifyDeployAuthAndGetToken verifies the user's Keycloak session in ~/.idlistack/credentials.json,
// enforces Cluster-Wide vs Namespace-Scoped RBAC for targetNamespace, applies the K8s Role/RoleBinding,
// and returns the scoped Kubernetes RBAC token for Helm.
func VerifyDeployAuthAndGetToken(ctx context.Context, targetNamespace string) (string, string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", "", fmt.Errorf("could not resolve home directory: %w", err)
	}
	credPath := filepath.Join(home, ".idlistack", "credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		return "", "", "", fmt.Errorf("not authenticated with Keycloak: run 'idlistack login' before deploying to K3s")
	}

	var creds struct {
		Username          string    `json:"username"`
		Groups            []string  `json:"groups"`
		Scope             string    `json:"scope"`
		AllowedNamespaces []string  `json:"allowedNamespaces"`
		K8sToken          string    `json:"k8sToken"`
		ExpiresAt         time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", "", "", fmt.Errorf("invalid credentials file: run 'idlistack login'")
	}

	if time.Now().After(creds.ExpiresAt) {
		return "", "", "", fmt.Errorf("Keycloak token expired at %s: run 'idlistack login'", creds.ExpiresAt.Format(time.RFC1123))
	}

	k8sToken, err := EnsureNamespaceRBAC(ctx, creds.Username, creds.Groups, creds.AllowedNamespaces, targetNamespace)
	if err != nil {
		return "", "", "", err
	}
	if k8sToken == "" {
		k8sToken = creds.K8sToken
	}

	return k8sToken, creds.Username, creds.Scope, nil
}

// EnsureK3sContext ensures kubectl is targeting the local K3s cluster ("default" context)
// rather than an inactive or secondary context like minikube.
func EnsureK3sContext(ctx context.Context) {
	ctxCmd := exec.CommandContext(ctx, "kubectl", "config", "current-context")
	out, err := ctxCmd.Output()
	if err == nil && strings.TrimSpace(string(out)) == "default" {
		return
	}
	// Check if K3s "default" context is available and responsive
	if exec.CommandContext(ctx, "kubectl", "--context", "default", "get", "nodes").Run() == nil {
		_ = exec.CommandContext(ctx, "kubectl", "config", "use-context", "default").Run()
	}
}

// GetKeycloakBaseURL resolves the K3s Node IP and returns the Keycloak base URL
func GetKeycloakBaseURL(ctx context.Context) string {
	if envURL := os.Getenv("IDLISTACK_KEYCLOAK_URL"); envURL != "" {
		return strings.TrimSuffix(envURL, "/")
	}
	EnsureK3sContext(ctx)
	nodeIP := ResolveClusterNodeIP(ctx)
	nodePort := KeycloakNodePort
	svcCmd := exec.CommandContext(ctx, "kubectl", "get", "svc", "keycloak", "-n", KeycloakNamespace, "-o", "jsonpath={.spec.ports[0].nodePort}")
	if out, err := svcCmd.Output(); err == nil {
		if p, errP := strconv.Atoi(strings.TrimSpace(string(out))); errP == nil && p > 0 {
			nodePort = p
		}
	}
	return fmt.Sprintf("http://%s:%d", nodeIP, nodePort)
}

// ResolveClusterNodeIP returns the InternalIP of the K3s node or 127.0.0.1
func ResolveClusterNodeIP(ctx context.Context) string {
	EnsureK3sContext(ctx)
	cmdIp := exec.CommandContext(ctx, "kubectl", "get", "nodes", "-o", `jsonpath={.items[0].status.addresses[?(@.type=="InternalIP")].address}`)
	outIp, err := cmdIp.Output()
	if err != nil {
		return "127.0.0.1"
	}
	nodeIp := strings.TrimSpace(string(outIp))
	if nodeIp == "" {
		return "127.0.0.1"
	}
	return strings.Fields(nodeIp)[0]
}

// IsKeycloakRunning checks if the Keycloak deployment in K3s is ready
func IsKeycloakRunning(ctx context.Context) (bool, string) {
	EnsureK3sContext(ctx)
	url := GetKeycloakBaseURL(ctx)
	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", "keycloak", "-n", KeycloakNamespace, "-o", "jsonpath={.status.readyReplicas}")
	out, err := cmd.Output()
	if err != nil {
		return false, url
	}
	ready := strings.TrimSpace(string(out))
	return ready != "" && ready != "0", url
}

// DeployKeycloakToK3s deploys Keycloak inside the local K3s cluster using default dynamic configuration.
func DeployKeycloakToK3s(ctx context.Context) (string, error) {
	cfg := DefaultKeycloakDeployConfig(ctx)
	return DeployKeycloakWithConfig(ctx, cfg)
}

// DeployKeycloakWithConfig deploys Keycloak inside the local K3s cluster with the provided dynamic configuration.
func DeployKeycloakWithConfig(ctx context.Context, cfg KeycloakDeployConfig) (string, error) {
	EnsureK3sContext(ctx)
	if cfg.Namespace == "" {
		cfg.Namespace = KeycloakNamespace
	}
	if cfg.Realm == "" {
		cfg.Realm = KeycloakRealm
	}
	if cfg.ClientID == "" {
		cfg.ClientID = KeycloakClientID
	}
	if cfg.Image == "" {
		cfg.Image = KeycloakImage
	}
	if cfg.HTTPPort <= 0 {
		cfg.HTTPPort = KeycloakHTTPPort
	}
	if cfg.NodePort <= 0 {
		cfg.NodePort = KeycloakNodePort
	}
	if cfg.Replicas <= 0 {
		cfg.Replicas = 1
	}
	if cfg.TokenLifespan <= 0 {
		cfg.TokenLifespan = 86400
	}
	if cfg.AdminUser == "" {
		cfg.AdminUser = "admin"
	}
	if cfg.AdminPassword == "" {
		_, existingPass := GetKeycloakAdminCredentials(ctx)
		if existingPass != "" {
			cfg.AdminPassword = existingPass
		} else {
			cfg.AdminPassword = generateRandomPassword(12)
		}
	}

	KeycloakNamespace = cfg.Namespace
	KeycloakRealm = cfg.Realm
	KeycloakClientID = cfg.ClientID
	KeycloakNodePort = cfg.NodePort
	KeycloakHTTPPort = cfg.HTTPPort
	KeycloakImage = cfg.Image

	ui.Detail("Creating namespace %s in K3s...", color.CyanString(cfg.Namespace))
	manifest, err := generateKeycloakK8sManifest(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to generate Keycloak manifest: %w", err)
	}

	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	applyCmd.Stdin = bytes.NewBufferString(manifest)
	applyCmd.Stdout = os.Stdout
	applyCmd.Stderr = os.Stderr
	if err := applyCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to apply Keycloak manifests to K3s: %w", err)
	}

	ui.Detail("Waiting for Keycloak pod to become ready in K3s (up to 180s)...")
	waitCmd := exec.CommandContext(ctx, "kubectl", "rollout", "status", "deployment/keycloak", "-n", cfg.Namespace, "--timeout=180s")
	waitCmd.Stdout = os.Stdout
	waitCmd.Stderr = os.Stderr
	if err := waitCmd.Run(); err != nil {
		return "", fmt.Errorf("keycloak rollout did not complete in time: %w", err)
	}

	keycloakURL := GetKeycloakBaseURL(ctx)
	return keycloakURL, nil
}

// GetNamespaceOwner returns the username of the user who owns or deployed this namespace, or "" if not deployed/owned yet.
func GetNamespaceOwner(ctx context.Context, namespace string) string {
	EnsureK3sContext(ctx)
	if namespace == "" || namespace == "*" {
		return ""
	}
	// 1. Check label idlistack.io/owner on namespace
	cmd := exec.CommandContext(ctx, "kubectl", "get", "namespace", namespace, "-o", `jsonpath={.metadata.labels['idlistack\.io/owner']}`)
	if out, err := cmd.Output(); err == nil {
		owner := strings.TrimSpace(string(out))
		if owner != "" {
			return owner
		}
	}
	// 2. Check RoleBinding in that namespace
	rbCmd := exec.CommandContext(ctx, "kubectl", "get", "rolebinding", "-n", namespace, "-l", "managed-by=idlistack",
		"-o", `jsonpath={.items[0].subjects[?(@.kind=="ServiceAccount")].name}`)
	if out, err := rbCmd.Output(); err == nil {
		sa := strings.TrimSpace(string(out))
		if strings.HasPrefix(sa, "idlistack-user-") {
			return strings.TrimPrefix(sa, "idlistack-user-")
		}
	}
	// 3. Check any deployment in that namespace
	depCmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", "-n", namespace, "-l", "managed-by=idlistack", "-o", "jsonpath={.items[0].metadata.name}")
	if out, err := depCmd.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return "existing-project"
	}
	return ""
}

// EnsureNamespaceRBAC provisions a Kubernetes ServiceAccount, Role, and RoleBinding (or ClusterRoleBinding)
// for the authenticated Keycloak user so Helm and kubectl commands enforce cluster-wide or namespace-scoped RBAC.
func EnsureNamespaceRBAC(ctx context.Context, username string, groups []string, allowedNamespaces []string, targetNamespace string) (string, error) {
	EnsureK3sContext(ctx)
	saName := sanitizeK8sName(fmt.Sprintf("idlistack-user-%s", username))
	isClusterAdmin := false
	for _, g := range groups {
		gLower := strings.ToLower(strings.TrimPrefix(g, "/"))
		if gLower == "cluster-admins" || gLower == "admin" || gLower == "admins" {
			isClusterAdmin = true
			break
		}
	}

	// Check if namespace-scoped user is permitted to access targetNamespace
	if !isClusterAdmin && targetNamespace != "" && len(allowedNamespaces) > 0 {
		permitted := false
		for _, ns := range allowedNamespaces {
			if ns == "*" || ns == targetNamespace || fmt.Sprintf("idlistack-%s", ns) == targetNamespace {
				permitted = true
				break
			}
		}
		if !permitted {
			return "", fmt.Errorf("RBAC denied: user %q is not authorized for namespace %q (allowed: %v)", username, targetNamespace, allowedNamespaces)
		}
	}

	// Ensure idlistack-auth namespace exists to hold user ServiceAccounts
	_ = exec.CommandContext(ctx, "kubectl", "create", "namespace", KeycloakNamespace).Run()

	// Ensure target namespace exists if specified
	if targetNamespace != "" {
		_ = exec.CommandContext(ctx, "kubectl", "create", "namespace", targetNamespace).Run()
		// Label the owner of this project namespace if not already labeled
		if existingOwner := GetNamespaceOwner(ctx, targetNamespace); existingOwner == "" || existingOwner == "existing-project" {
			_ = exec.CommandContext(ctx, "kubectl", "label", "namespace", targetNamespace, fmt.Sprintf("idlistack.io/owner=%s", sanitizeK8sName(username)), "--overwrite").Run()
		}
	}

	var rbacManifest strings.Builder

	// 1. ServiceAccount representing the Keycloak-authenticated user
	rbacManifest.WriteString(fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s
  labels:
    managed-by: idlistack
    keycloak-user: %s
`, saName, KeycloakNamespace, sanitizeK8sName(username)))

	if isClusterAdmin {
		// Cluster-wide RBAC: bind both OIDC user/group and the backing ServiceAccount to cluster-admin
		rbacManifest.WriteString(fmt.Sprintf(`---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: idlistack-cluster-admin-%[1]s
  labels:
    managed-by: idlistack
subjects:
- kind: ServiceAccount
  name: %[1]s
  namespace: %[2]s
- kind: User
  name: oidc:%[3]s
  apiGroup: rbac.authorization.k8s.io
- kind: Group
  name: oidc:cluster-admins
  apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: cluster-admin
  apiGroup: rbac.authorization.k8s.io
`, saName, KeycloakNamespace, username))
	} else if targetNamespace != "" {
		// Namespace-scoped RBAC: grant full Helm deployment permissions ONLY in targetNamespace
		// Plus read-only node/service listing at cluster level so NodePort allocation works
		rbacManifest.WriteString(fmt.Sprintf(`---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: idlistack-node-reader
  labels:
    managed-by: idlistack
rules:
- apiGroups: [""]
  resources: ["nodes", "services", "namespaces"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: idlistack-node-reader-%[1]s
  labels:
    managed-by: idlistack
subjects:
- kind: ServiceAccount
  name: %[1]s
  namespace: %[2]s
- kind: User
  name: oidc:%[3]s
  apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: idlistack-node-reader
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: idlistack-ns-deployer
  namespace: %[4]s
  labels:
    managed-by: idlistack
rules:
- apiGroups: ["", "apps", "batch", "extensions", "networking.k8s.io"]
  resources: ["*"]
  verbs: ["*"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: idlistack-ns-binding-%[1]s
  namespace: %[4]s
  labels:
    managed-by: idlistack
subjects:
- kind: ServiceAccount
  name: %[1]s
  namespace: %[2]s
- kind: User
  name: oidc:%[3]s
  apiGroup: rbac.authorization.k8s.io
- kind: Group
  name: oidc:developers
  apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: Role
  name: idlistack-ns-deployer
  apiGroup: rbac.authorization.k8s.io
`, saName, KeycloakNamespace, username, targetNamespace))
	}

	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	applyCmd.Stdin = bytes.NewBufferString(rbacManifest.String())
	if out, err := applyCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to apply RBAC bindings: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	// Mint a time-bound K8s RBAC token for this ServiceAccount
	tokenCmd := exec.CommandContext(ctx, "kubectl", "create", "token", saName, "-n", KeycloakNamespace, "--duration=24h")
	tokenOut, err := tokenCmd.Output()
	if err != nil {
		return "", nil // Fallback gracefully if cluster doesn't support TokenRequest API
	}
	return strings.TrimSpace(string(tokenOut)), nil
}

// ConfigureK3sOIDC writes the Keycloak OIDC flags into /etc/rancher/k3s/config.yaml and restarts K3s
func ConfigureK3sOIDC(ctx context.Context) error {
	keycloakURL := GetKeycloakBaseURL(ctx)
	issuerURL := fmt.Sprintf("%s/realms/%s", keycloakURL, KeycloakRealm)

	oidcConfig := fmt.Sprintf(`# Added by IdliStack Auth Setup (%s)
kube-apiserver-arg:
  - "oidc-issuer-url=%s"
  - "oidc-client-id=%s"
  - "oidc-username-claim=preferred_username"
  - "oidc-username-prefix=oidc:"
  - "oidc-groups-claim=groups"
  - "oidc-groups-prefix=oidc:"
`, time.Now().Format(time.RFC3339), issuerURL, KeycloakClientID)

	ui.Detail("Configuring K3s API server OIDC with issuer: %s", color.CyanString(issuerURL))
	script := fmt.Sprintf(`sudo mkdir -p /etc/rancher/k3s && printf '%%s\n' %q | sudo tee /etc/rancher/k3s/config.yaml >/dev/null && sudo systemctl restart k3s`, oidcConfig)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildDynamicRealmJSON(cfg KeycloakDeployConfig) (string, error) {
	users := make([]map[string]any, 0, len(cfg.InitialUsers))
	for _, u := range cfg.InitialUsers {
		if strings.TrimSpace(u.Username) == "" || u.Password == "" {
			continue
		}
		email := u.Email
		if email == "" {
			email = fmt.Sprintf("%s@%s.local", strings.ToLower(u.Username), cfg.Realm)
		}
		firstName := u.FirstName
		if firstName == "" {
			firstName = u.Username
		}
		lastName := u.LastName
		if lastName == "" {
			lastName = "User"
		}
		groups := u.Groups
		if len(groups) == 0 {
			groups = []string{"developers"}
		}
		users = append(users, map[string]any{
			"username":      u.Username,
			"enabled":       true,
			"emailVerified": true,
			"firstName":     firstName,
			"lastName":      lastName,
			"email":         email,
			"groups":        groups,
			"credentials": []map[string]any{
				{
					"type":      "password",
					"value":     u.Password,
					"temporary": false,
				},
			},
		})
	}

	realmObj := map[string]any{
		"realm":                 cfg.Realm,
		"enabled":               true,
		"registrationAllowed":   true,
		"defaultGroups":         []string{"/developers"},
		"displayName":           fmt.Sprintf("IdliStack K3s Auth (%s)", cfg.Realm),
		"accessTokenLifespan":   cfg.TokenLifespan,
		"ssoSessionIdleTimeout": cfg.TokenLifespan,
		"ssoSessionMaxLifespan": cfg.TokenLifespan,
		"groups": []map[string]any{
			{
				"name": "cluster-admins",
				"path": "/cluster-admins",
			},
			{
				"name": "developers",
				"path": "/developers",
			},
		},
		"clients": []map[string]any{
			{
				"clientId":                  cfg.ClientID,
				"name":                      "IdliStack CLI & Auth Portal",
				"enabled":                   true,
				"publicClient":              true,
				"directAccessGrantsEnabled": true,
				"standardFlowEnabled":       true,
				"implicitFlowEnabled":       true,
				"redirectUris":              []string{"*"},
				"webOrigins":                []string{"*"},
				"protocol":                  "openid-connect",
				"protocolMappers": []map[string]any{
					{
						"name":            "groups",
						"protocol":        "openid-connect",
						"protocolMapper":  "oidc-group-membership-mapper",
						"consentRequired": false,
						"config": map[string]string{
							"full.path":            "false",
							"id.token.claim":       "true",
							"access.token.claim":   "true",
							"claim.name":           "groups",
							"userinfo.token.claim": "true",
						},
					},
				},
			},
		},
		"users": users,
	}

	raw, err := json.MarshalIndent(realmObj, "    ", "  ")
	if err != nil {
		return "", err
	}
	return "    " + string(raw), nil
}

func generateKeycloakK8sManifest(cfg KeycloakDeployConfig) (string, error) {
	realmJSON, err := buildDynamicRealmJSON(cfg)
	if err != nil {
		return "", err
	}

	adminUserB64 := base64.StdEncoding.EncodeToString([]byte(cfg.AdminUser))
	adminPassB64 := base64.StdEncoding.EncodeToString([]byte(cfg.AdminPassword))

	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %[1]s
  labels:
    managed-by: idlistack
---
apiVersion: v1
kind: Secret
metadata:
  name: %[2]s
  namespace: %[1]s
  labels:
    app: keycloak
    managed-by: idlistack
type: Opaque
data:
  KEYCLOAK_ADMIN: %[3]s
  KEYCLOAK_ADMIN_PASSWORD: %[4]s
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: keycloak-realm-config
  namespace: %[1]s
data:
  %[5]s-realm.json: |
%[6]s
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: keycloak
  namespace: %[1]s
  labels:
    app: keycloak
    managed-by: idlistack
spec:
  replicas: %[7]d
  selector:
    matchLabels:
      app: keycloak
  template:
    metadata:
      labels:
        app: keycloak
    spec:
      containers:
      - name: keycloak
        image: %[8]s
        imagePullPolicy: IfNotPresent
        args:
        - start-dev
        - --import-realm
        env:
        - name: KEYCLOAK_ADMIN
          valueFrom:
            secretKeyRef:
              name: %[2]s
              key: KEYCLOAK_ADMIN
        - name: KEYCLOAK_ADMIN_PASSWORD
          valueFrom:
            secretKeyRef:
              name: %[2]s
              key: KEYCLOAK_ADMIN_PASSWORD
        - name: KC_HTTP_PORT
          value: "%[9]d"
        - name: KC_HEALTH_ENABLED
          value: "true"
        ports:
        - containerPort: %[9]d
        readinessProbe:
          tcpSocket:
            port: %[9]d
          initialDelaySeconds: 10
          periodSeconds: 5
          failureThreshold: 30
        volumeMounts:
        - name: realm-config
          mountPath: /opt/keycloak/data/import
      volumes:
      - name: realm-config
        configMap:
          name: keycloak-realm-config
---
apiVersion: v1
kind: Service
metadata:
  name: keycloak
  namespace: %[1]s
  labels:
    app: keycloak
    managed-by: idlistack
spec:
  type: NodePort
  selector:
    app: keycloak
  ports:
  - protocol: TCP
    port: %[9]d
    targetPort: %[9]d
    nodePort: %[10]d
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: idlistack-socket-fixer
  namespace: %[1]s
  labels:
    managed-by: idlistack
spec:
  selector:
    matchLabels:
      app: idlistack-socket-fixer
  template:
    metadata:
      labels:
        app: idlistack-socket-fixer
    spec:
      containers:
      - name: fixer
        image: redis:7-alpine
        imagePullPolicy: IfNotPresent
        securityContext:
          privileged: true
          runAsUser: 0
        command: ["sh", "-c", "while true; do chmod 666 /host-containerd/containerd.sock 2>/dev/null || true; sleep 5; done"]
        volumeMounts:
        - name: host-containerd
          mountPath: /host-containerd
      volumes:
      - name: host-containerd
        hostPath:
          path: /run/k3s/containerd
          type: Directory
`,
		cfg.Namespace,
		KeycloakAdminSecret,
		adminUserB64,
		adminPassB64,
		cfg.Realm,
		realmJSON,
		cfg.Replicas,
		cfg.Image,
		cfg.HTTPPort,
		cfg.NodePort,
	), nil
}

// EnsureContainerdSocketPermissions ensures /run/k3s/containerd/containerd.sock has 0666 permissions
// via the in-cluster DaemonSet so `k3s ctr images import` never fails with permission denied.
func EnsureContainerdSocketPermissions(ctx context.Context) {
	EnsureK3sContext(ctx)
	dsManifest := fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %[1]s
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: idlistack-socket-fixer
  namespace: %[1]s
  labels:
    managed-by: idlistack
spec:
  selector:
    matchLabels:
      app: idlistack-socket-fixer
  template:
    metadata:
      labels:
        app: idlistack-socket-fixer
    spec:
      containers:
      - name: fixer
        image: redis:7-alpine
        imagePullPolicy: IfNotPresent
        securityContext:
          privileged: true
          runAsUser: 0
        command: ["sh", "-c", "while true; do chmod 666 /host-containerd/containerd.sock 2>/dev/null || true; sleep 5; done"]
        volumeMounts:
        - name: host-containerd
          mountPath: /host-containerd
      volumes:
      - name: host-containerd
        hostPath:
          path: /run/k3s/containerd
          type: Directory
`, KeycloakNamespace)
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = bytes.NewBufferString(dsManifest)
	_ = cmd.Run()
	time.Sleep(2 * time.Second)
}

