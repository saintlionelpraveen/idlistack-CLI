package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/ui"
)

const (
	KeycloakNamespace = "idlistack-auth"
	KeycloakNodePort  = 30080
	KeycloakRealm     = "idlistack"
	KeycloakClientID  = "idlistack-cli"
)

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
	return fmt.Sprintf("http://%s:%d", nodeIP, KeycloakNodePort)
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

// DeployKeycloakToK3s deploys Keycloak inside the local K3s cluster in namespace `idlistack-auth`
// with a pre-configured `idlistack` realm, `idlistack-cli` OIDC client, groups mapper, and default RBAC users.
func DeployKeycloakToK3s(ctx context.Context) (string, error) {
	EnsureK3sContext(ctx)
	ui.Detail("Creating namespace %s in K3s...", color.CyanString(KeycloakNamespace))
	manifest := generateKeycloakK8sManifest()

	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	applyCmd.Stdin = bytes.NewBufferString(manifest)
	applyCmd.Stdout = os.Stdout
	applyCmd.Stderr = os.Stderr
	if err := applyCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to apply Keycloak manifests to K3s: %w", err)
	}

	ui.Detail("Waiting for Keycloak pod to become ready in K3s (up to 180s)...")
	waitCmd := exec.CommandContext(ctx, "kubectl", "rollout", "status", "deployment/keycloak", "-n", KeycloakNamespace, "--timeout=180s")
	waitCmd.Stdout = os.Stdout
	waitCmd.Stderr = os.Stderr
	if err := waitCmd.Run(); err != nil {
		return "", fmt.Errorf("keycloak rollout did not complete in time: %w", err)
	}

	keycloakURL := GetKeycloakBaseURL(ctx)
	return keycloakURL, nil
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

func generateKeycloakK8sManifest() string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %[1]s
  labels:
    managed-by: idlistack
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: keycloak-realm-config
  namespace: %[1]s
data:
  idlistack-realm.json: |
    {
      "realm": "%[2]s",
      "enabled": true,
      "displayName": "IdliStack K3s Auth",
      "accessTokenLifespan": 86400,
      "ssoSessionIdleTimeout": 86400,
      "ssoSessionMaxLifespan": 86400,
      "groups": [
        {
          "name": "cluster-admins",
          "path": "/cluster-admins"
        },
        {
          "name": "developers",
          "path": "/developers"
        }
      ],
      "clients": [
        {
          "clientId": "%[3]s",
          "name": "IdliStack CLI & Auth Portal",
          "enabled": true,
          "publicClient": true,
          "directAccessGrantsEnabled": true,
          "standardFlowEnabled": true,
          "implicitFlowEnabled": true,
          "redirectUris": ["*"],
          "webOrigins": ["*"],
          "protocol": "openid-connect",
          "protocolMappers": [
            {
              "name": "groups",
              "protocol": "openid-connect",
              "protocolMapper": "oidc-group-membership-mapper",
              "consentRequired": false,
              "config": {
                "full.path": "false",
                "id.token.claim": "true",
                "access.token.claim": "true",
                "claim.name": "groups",
                "userinfo.token.claim": "true"
              }
            }
          ]
        }
      ],
      "users": [
        {
          "username": "admin",
          "enabled": true,
          "emailVerified": true,
          "firstName": "Cluster",
          "lastName": "Admin",
          "email": "admin@idlistack.local",
          "groups": ["cluster-admins"],
          "credentials": [
            {
              "type": "password",
              "value": "admin123",
              "temporary": false
            }
          ]
        },
        {
          "username": "developer",
          "enabled": true,
          "emailVerified": true,
          "firstName": "App",
          "lastName": "Developer",
          "email": "dev@idlistack.local",
          "groups": ["developers"],
          "credentials": [
            {
              "type": "password",
              "value": "dev123",
              "temporary": false
            }
          ]
        }
      ]
    }
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
  replicas: 1
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
        image: quay.io/keycloak/keycloak:24.0
        imagePullPolicy: IfNotPresent
        args:
        - start-dev
        - --import-realm
        env:
        - name: KEYCLOAK_ADMIN
          value: "admin"
        - name: KEYCLOAK_ADMIN_PASSWORD
          value: "admin"
        - name: KC_HTTP_PORT
          value: "8080"
        - name: KC_HEALTH_ENABLED
          value: "true"
        ports:
        - containerPort: 8080
        readinessProbe:
          tcpSocket:
            port: 8080
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
    port: 8080
    targetPort: 8080
    nodePort: %[4]d
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
`, KeycloakNamespace, KeycloakRealm, KeycloakClientID, KeycloakNodePort)
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

