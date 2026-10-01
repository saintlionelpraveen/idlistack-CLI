package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/k8s"
)

var validUsernameRegex = regexp.MustCompile(`^[a-zA-Z0-9._-]{2,64}$`)

func validateAuthInput(username, password string) error {
	username = strings.TrimSpace(username)
	if !validUsernameRegex.MatchString(username) {
		return fmt.Errorf("username must be 2-64 characters and contain only letters, numbers, hyphens, periods, or underscores")
	}
	if len(password) < 4 {
		return fmt.Errorf("password must be at least 4 characters")
	}
	if len(password) > 256 {
		return fmt.Errorf("password cannot exceed 256 characters")
	}
	return nil
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// PortalServer serves the local Keycloak + K3s RBAC Authentication Frontend
type PortalServer struct {
	port           int
	defaultProject string
	listener       net.Listener
	httpServer     *http.Server
	loginComplete  chan *Credentials
}

// NewPortalServer creates a new authentication frontend server on an available port
func NewPortalServer(preferredPort int, defaultProject string) (*PortalServer, error) {
	if preferredPort <= 0 {
		preferredPort = 4201
	}

	var listener net.Listener
	var actualPort int
	for p := preferredPort; p < preferredPort+50; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			listener = l
			actualPort = p
			break
		}
	}
	if listener == nil {
		return nil, fmt.Errorf("could not bind auth portal port between %d and %d", preferredPort, preferredPort+50)
	}

	ps := &PortalServer{
		port:           actualPort,
		defaultProject: defaultProject,
		listener:       listener,
		loginComplete:  make(chan *Credentials, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", ps.handleIndex)
	mux.HandleFunc("/api/auth/status", ps.handleStatus)
	mux.HandleFunc("/api/auth/login", ps.handleLogin)
	mux.HandleFunc("/api/auth/register", ps.handleRegister)
	mux.HandleFunc("/api/auth/token-exchange", ps.handleTokenExchange)
	mux.HandleFunc("/api/auth/logout", ps.handleLogout)
	mux.HandleFunc("/api/auth/deploy-keycloak", ps.handleDeployKeycloak)

	ps.httpServer = &http.Server{
		Handler:      secureHeaders(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 180 * time.Second,
	}

	return ps, nil
}

func (ps *PortalServer) Start() {
	go func() {
		_ = ps.httpServer.Serve(ps.listener)
	}()
}

func (ps *PortalServer) Stop(ctx context.Context) error {
	return ps.httpServer.Shutdown(ctx)
}

func (ps *PortalServer) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", ps.port)
}

func (ps *PortalServer) LoginCompleteChan() <-chan *Credentials {
	return ps.loginComplete
}

func (ps *PortalServer) SetDefaultProject(project string) {
	if project != "" {
		ps.defaultProject = project
	}
}

func OpenBrowserURL(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", target)
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return fmt.Errorf("unsupported OS")
	}
	return cmd.Start()
}

func (ps *PortalServer) OpenBrowser() error {
	return OpenBrowserURL(ps.URL())
}

func (ps *PortalServer) resolveDefaultProject(r *http.Request) string {
	if q := strings.TrimSpace(r.URL.Query().Get("project")); q != "" {
		ps.defaultProject = q
		return q
	}
	if ps.defaultProject != "" {
		return ps.defaultProject
	}
	cwd, err := os.Getwd()
	if err == nil {
		if cfg, err := config.Load(cwd); err == nil && cfg.Project.Name != "" {
			return cfg.Project.Name
		}
		return filepath.Base(cwd)
	}
	return "app"
}

func (ps *PortalServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	project := ps.resolveDefaultProject(r)
	targetNS := "idlistack-" + strings.ToLower(project)
	kcRunning, kcURL := k8s.IsKeycloakRunning(ctx)
	owner := k8s.GetNamespaceOwner(ctx, targetNS)
	isDeployed := owner != ""
	if owner != "" && owner != "existing-project" {
		if !UserExistsInKeycloak(ctx, owner) {
			// Owner was deleted in Keycloak: clear orphaned ownership so the project can be reclaimed!
			_ = exec.CommandContext(ctx, "kubectl", "label", "namespace", targetNS, "idlistack.io/owner-").Run()
			owner = ""
			isDeployed = false
		}
	}
	creds, _ := LoadCredentials()

	var activeSession any
	if creds != nil && time.Now().Before(creds.ExpiresAt) {
		activeSession = map[string]any{
			"username":          creds.Username,
			"email":             creds.Email,
			"groups":            creds.Groups,
			"scope":             creds.Scope,
			"allowedNamespaces": creds.AllowedNamespaces,
			"keycloakUrl":       creds.KeycloakURL,
			"realm":             creds.Realm,
			"issuedAt":          creds.IssuedAt.Format(time.RFC3339),
			"expiresAt":         creds.ExpiresAt.Format(time.RFC3339),
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"keycloakRunning": kcRunning,
		"keycloakUrl":     kcURL,
		"realm":           k8s.KeycloakRealm,
		"clientId":        k8s.KeycloakClientID,
		"defaultProject":  project,
		"namespace":       targetNS,
		"projectOwner":    owner,
		"isDeployed":      isDeployed,
		"session":         activeSession,
	})
}

func (ps *PortalServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB payload limit
	var req struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		Namespace string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid request format"})
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if !validUsernameRegex.MatchString(req.Username) {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid username format"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	kcURL := k8s.GetKeycloakBaseURL(ctx)
	ns := strings.TrimSpace(req.Namespace)
	if ns == "" && ps.defaultProject != "" {
		ns = "idlistack-" + strings.ToLower(ps.defaultProject)
	}
	var allowedNS []string
	if ns != "" {
		allowedNS = []string{ns}
	}

	creds, err := LoginWithKeycloak(ctx, kcURL, k8s.KeycloakRealm, k8s.KeycloakClientID, req.Username, req.Password, allowedNS)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}

	select {
	case ps.loginComplete <- creds:
	default:
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"session": creds,
	})
}

func (ps *PortalServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB payload limit
	var req struct {
		Username  string `json:"username"`
		Email     string `json:"email"`
		Password  string `json:"password"`
		Namespace string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid request format"})
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if err := validateAuthInput(req.Username, req.Password); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	ns := strings.TrimSpace(req.Namespace)
	if ns == "" && ps.defaultProject != "" {
		ns = "idlistack-" + strings.ToLower(ps.defaultProject)
	}

	// Security Gate: If project is already deployed/owned, prevent unauthorized user from hijacking it
	if ns != "" {
		owner := k8s.GetNamespaceOwner(ctx, ns)
		if owner != "" && owner != "existing-project" {
			if !UserExistsInKeycloak(ctx, owner) {
				// Previous owner was deleted in Keycloak: clear orphaned ownership
				_ = exec.CommandContext(ctx, "kubectl", "label", "namespace", ns, "idlistack.io/owner-").Run()
			} else if !strings.EqualFold(owner, req.Username) {
				json.NewEncoder(w).Encode(map[string]any{
					"ok":    false,
					"error": fmt.Sprintf("Project '%s' is already deployed and owned by active user '%s'. Please sign in with the authorized account.", ns, owner),
				})
				return
			}
		}
	}

	// Strictly namespace-scoped developer role (never cluster-admin via portal)
	creds, err := RegisterAndLoginKeycloakUser(ctx, req.Username, req.Email, req.Password, "developer", ns)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}

	select {
	case ps.loginComplete <- creds:
	default:
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"session": creds,
	})
}

func (ps *PortalServer) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		AccessToken string `json:"accessToken"`
		IDToken     string `json:"idToken"`
		ExpiresIn   int    `json:"expiresIn"`
		Namespace   string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid request"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	kcURL := k8s.GetKeycloakBaseURL(ctx)
	var allowedNS []string
	if req.Namespace != "" {
		allowedNS = []string{req.Namespace}
	} else if ps.defaultProject != "" {
		allowedNS = []string{"idlistack-" + strings.ToLower(ps.defaultProject)}
	}

	creds, err := BuildAndSaveCredentialsFromTokens(ctx, kcURL, k8s.KeycloakRealm, k8s.KeycloakClientID, req.AccessToken, req.IDToken, "", req.ExpiresIn, allowedNS)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}

	select {
	case ps.loginComplete <- creds:
	default:
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"session": creds,
	})
}

func (ps *PortalServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = ClearCredentials()
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (ps *PortalServer) handleDeployKeycloak(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	kcURL, err := k8s.DeployKeycloakToK3s(ctx)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":          true,
		"keycloakUrl": strings.TrimSuffix(kcURL, "/"),
	})
}

func (ps *PortalServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if q := strings.TrimSpace(r.URL.Query().Get("project")); q != "" {
		ps.defaultProject = q
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write([]byte(authPortalHTML))
}

const authPortalHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>IdliStack Auth — Keycloak K3s</title>
  <style>
    :root {
      --bg: #08090d;
      --card: #0f121a;
      --border: #1e2434;
      --border-focus: #3b82f6;
      --input-bg: #090b10;
      --input-border: #22293a;
      --text: #ffffff;
      --text-dim: #cbd5e1;
      --muted: #94a3b8;
      --blue: #2563eb;
      --blue-hover: #1d4ed8;
      --blue-glow: rgba(37, 99, 235, 0.25);
      --green: #10b981;
      --green-bg: rgba(16, 185, 129, 0.1);
      --red: #ef4444;
      --red-bg: rgba(239, 68, 68, 0.12);
      --amber: #f59e0b;
      --amber-bg: rgba(245, 158, 11, 0.1);
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Inter, Roboto, sans-serif;
      background: var(--bg);
      color: var(--text);
      min-height: 100vh;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 20px;
    }
    .card {
      width: 100%;
      max-width: 420px;
      background: var(--card);
      border: 1px solid var(--border);
      border-radius: 14px;
      padding: 32px 28px;
      box-shadow: 0 24px 48px -12px rgba(0, 0, 0, 0.8), 0 0 0 1px rgba(255, 255, 255, 0.04);
    }
    .brand-header {
      display: flex;
      flex-direction: column;
      align-items: center;
      margin-bottom: 22px;
      text-align: center;
    }
    .keycloak-badge {
      display: inline-flex;
      align-items: center;
      gap: 7px;
      padding: 6px 12px;
      background: #141926;
      border: 1px solid var(--border);
      border-radius: 9999px;
      font-size: 12px;
      font-weight: 500;
      color: var(--text-dim);
      margin-bottom: 12px;
    }
    .keycloak-badge svg {
      width: 15px;
      height: 15px;
      flex-shrink: 0;
    }
    h1 {
      font-size: 21px;
      font-weight: 700;
      letter-spacing: -0.02em;
      color: var(--text);
      margin-bottom: 4px;
    }
    .subtitle {
      font-size: 13px;
      color: var(--muted);
    }
    .tabs {
      display: grid;
      grid-template-columns: 1fr 1fr;
      background: #090b10;
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 3px;
      margin-bottom: 16px;
    }
    .tab {
      padding: 8px;
      border: none;
      background: transparent;
      color: var(--muted);
      font-size: 13px;
      font-weight: 600;
      border-radius: 6px;
      cursor: pointer;
      transition: all 0.15s ease;
    }
    .tab.active {
      background: #1c2333;
      color: var(--text);
    }
    .ns-pill {
      display: flex;
      align-items: center;
      justify-content: space-between;
      background: #090b10;
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 8px 12px;
      font-size: 12px;
      margin-bottom: 16px;
    }
    .ns-pill span:first-child { color: var(--muted); }
    .ns-pill code { color: #60a5fa; font-weight: 600; font-family: monospace; }
    
    .owner-lock-banner {
      background: var(--amber-bg);
      border: 1px solid rgba(245, 158, 11, 0.35);
      color: #fde68a;
      border-radius: 8px;
      padding: 10px 12px;
      font-size: 12px;
      line-height: 1.45;
      margin-bottom: 16px;
      display: none;
    }

    .field { margin-bottom: 14px; }
    label {
      display: block;
      font-size: 12px;
      font-weight: 500;
      color: var(--muted);
      margin-bottom: 6px;
    }
    input {
      width: 100%;
      padding: 11px 12px;
      background: var(--input-bg);
      border: 1px solid var(--input-border);
      border-radius: 8px;
      color: var(--text);
      font-size: 14px;
      outline: none;
      transition: all 0.15s ease;
    }
    input:focus {
      border-color: var(--border-focus);
      box-shadow: 0 0 0 3px var(--blue-glow);
    }
    .scope-notice {
      display: flex;
      align-items: center;
      gap: 6px;
      font-size: 11.5px;
      color: var(--muted);
      margin-top: -6px;
      margin-bottom: 14px;
      padding: 6px 10px;
      background: #090b10;
      border: 1px dashed var(--border);
      border-radius: 6px;
    }
    .scope-notice strong { color: #60a5fa; }

    .btn {
      width: 100%;
      padding: 12px 14px;
      border-radius: 8px;
      border: none;
      font-size: 14px;
      font-weight: 600;
      cursor: pointer;
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 8px;
      transition: all 0.15s ease;
    }
    .btn:disabled { opacity: 0.6; cursor: not-allowed; }
    .btn-primary {
      background: var(--blue);
      color: #ffffff;
      margin-top: 4px;
    }
    .btn-primary:hover:not(:disabled) {
      background: var(--blue-hover);
    }
    .divider {
      display: flex;
      align-items: center;
      gap: 12px;
      margin: 16px 0;
      color: #475569;
      font-size: 11px;
      text-transform: uppercase;
      letter-spacing: 0.05em;
    }
    .divider::before, .divider::after {
      content: "";
      flex: 1;
      height: 1px;
      background: var(--border);
    }
    .btn-keycloak {
      background: #111520;
      color: var(--text);
      border: 1px solid var(--border);
      font-size: 13.5px;
    }
    .btn-keycloak:hover {
      background: #171d2b;
      border-color: #3b82f6;
    }
    .btn-keycloak svg {
      width: 18px;
      height: 18px;
      flex-shrink: 0;
    }
    .msg {
      margin-top: 14px;
      padding: 10px 12px;
      border-radius: 8px;
      font-size: 13px;
      text-align: center;
      display: none;
    }
    .msg-error {
      background: var(--red-bg);
      border: 1px solid rgba(239, 68, 68, 0.4);
      color: #fca5a5;
    }

    /* Success / Active Session Card */
    .success-icon {
      width: 44px;
      height: 44px;
      border-radius: 50%;
      background: var(--green-bg);
      border: 1px solid rgba(16, 185, 129, 0.4);
      color: #34d399;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 20px;
      margin: 0 auto 12px;
    }
    .info-box {
      background: #090b10;
      border: 1px solid var(--border);
      border-radius: 10px;
      padding: 14px;
      margin: 18px 0;
    }
    .info-row {
      display: flex;
      justify-content: space-between;
      font-size: 13px;
      padding: 6px 0;
    }
    .info-row span:first-child { color: var(--muted); }
    .info-row span:last-child { font-weight: 600; color: var(--text); }
    .hint {
      font-size: 12px;
      color: var(--muted);
      text-align: center;
      line-height: 1.5;
      margin-bottom: 14px;
    }
    .btn-outline {
      background: transparent;
      color: var(--muted);
      border: 1px solid var(--border);
      margin-top: 8px;
      font-size: 13px;
    }
    .btn-outline:hover {
      color: var(--text);
      border-color: #3b82f6;
    }
  </style>
</head>
<body>
  <div class="card">
    <div id="authView">
      <div class="brand-header">
        <div class="keycloak-badge">
          <!-- Official Keycloak Shield Icon -->
          <svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
            <path d="M12 2L4 5.5V11.5C4 16.5 7.4 21.1 12 22.3C16.6 21.1 20 16.5 20 11.5V5.5L12 2Z" fill="#1E293B" stroke="#3B82F6" stroke-width="1.8" stroke-linejoin="round"/>
            <path d="M12 8C10.6 8 9.5 9.1 9.5 10.5C9.5 11.6 10.2 12.5 11.2 12.8V15.5H12.8V12.8C13.8 12.5 14.5 11.6 14.5 10.5C14.5 9.1 13.4 8 12 8Z" fill="#3B82F6"/>
          </svg>
          <span>Keycloak K3s IAM</span>
        </div>
        <h1>IdliStack Auth</h1>
        <p class="subtitle">Namespace-Scoped Kubernetes RBAC</p>
      </div>

      <div class="tabs">
        <button type="button" class="tab active" id="tabLogin" onclick="setMode('login')">Sign In</button>
        <button type="button" class="tab" id="tabRegister" onclick="setMode('register')">Create User</button>
      </div>

      <div class="ns-pill">
        <span>Target Namespace</span>
        <code id="nsDisplay">idlistack-app</code>
      </div>

      <!-- Project Ownership Lock Banner -->
      <div id="ownerLockBanner" class="owner-lock-banner">
        🔒 <strong>Deployed Project:</strong> Owned by <span id="ownerName" style="text-decoration:underline;"></span>. Sign in with this account to deploy updates.
      </div>

      <!-- Sign In Form (Default) -->
      <form id="loginForm" onsubmit="handleLogin(event)">
        <div class="field">
          <label>Username</label>
          <input type="text" id="username" placeholder="Username" required autocomplete="username" />
        </div>
        <div class="field">
          <label>Password</label>
          <input type="password" id="password" placeholder="••••••••" required autocomplete="current-password" />
        </div>
        <button type="submit" class="btn btn-primary" id="submitBtn">Sign In</button>
      </form>

      <!-- Namespace-Scoped Registration Form (Strictly Project-Scoped) -->
      <form id="registerForm" style="display:none;" onsubmit="handleRegister(event)">
        <div class="field">
          <label>New Username</label>
          <input type="text" id="regUsername" placeholder="e.g. praveen" required autocomplete="username" />
        </div>
        <div class="field">
          <label>Password</label>
          <input type="password" id="regPassword" placeholder="Create password" required autocomplete="new-password" />
        </div>
        <div class="scope-notice">
          <span>🛡️</span>
          <span>Access Level: <strong>Namespace-Scoped</strong> (this project only)</span>
        </div>
        <button type="submit" class="btn btn-primary" id="regBtn">Create User & Sign In</button>
      </form>

      <div class="divider">or</div>

      <!-- Keycloak SSO Button with Official Keycloak Icon -->
      <button type="button" class="btn btn-keycloak" onclick="openKeycloakSSO()">
        <svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
          <path d="M12 2L4 5.5V11.5C4 16.5 7.4 21.1 12 22.3C16.6 21.1 20 16.5 20 11.5V5.5L12 2Z" fill="#1E293B" stroke="#3B82F6" stroke-width="1.8" stroke-linejoin="round"/>
          <path d="M12 8C10.6 8 9.5 9.1 9.5 10.5C9.5 11.6 10.2 12.5 11.2 12.8V15.5H12.8V12.8C13.8 12.5 14.5 11.6 14.5 10.5C14.5 9.1 13.4 8 12 8Z" fill="#3B82F6"/>
        </svg>
        <span>Continue with Keycloak SSO ↗</span>
      </button>

      <div id="errorMsg" class="msg msg-error"></div>
    </div>

    <!-- Active Signed-In Session View -->
    <div id="successView" style="display:none;">
      <div class="success-icon">✓</div>
      <h1 style="text-align:center;">Authenticated</h1>
      <p class="subtitle" style="text-align:center;">Your K3s RBAC session is active</p>

      <div class="info-box">
        <div class="info-row">
          <span>User</span>
          <span id="okUser">-</span>
        </div>
        <div class="info-row">
          <span>RBAC Scope</span>
          <span id="okScope" style="color:#60a5fa;">Namespace-Scoped</span>
        </div>
        <div class="info-row">
          <span>Namespace</span>
          <span id="okNs" style="color:#ffffff; font-family:monospace;">-</span>
        </div>
      </div>

      <p class="hint">You can close this browser tab and return to your terminal.</p>

      <button type="button" class="btn btn-outline" onclick="switchAccount()">
        Sign In as Different User
      </button>
    </div>
  </div>

  <script>
    let state = {
      keycloakUrl: '',
      realm: 'idlistack',
      clientId: 'idlistack-cli',
      namespace: 'idlistack-app',
      projectOwner: ''
    };

    function setMode(mode) {
      document.getElementById('errorMsg').style.display = 'none';
      if (mode === 'register') {
        if (state.projectOwner) {
          const errBox = document.getElementById('errorMsg');
          errBox.textContent = 'This project is already owned by ' + state.projectOwner + '. Only the owner can deploy updates.';
          errBox.style.display = 'block';
          return;
        }
        document.getElementById('tabLogin').className = 'tab';
        document.getElementById('tabRegister').className = 'tab active';
        document.getElementById('loginForm').style.display = 'none';
        document.getElementById('registerForm').style.display = 'block';
      } else {
        document.getElementById('tabLogin').className = 'tab active';
        document.getElementById('tabRegister').className = 'tab';
        document.getElementById('loginForm').style.display = 'block';
        document.getElementById('registerForm').style.display = 'none';
      }
    }

    async function init() {
      try {
        const res = await fetch('/api/auth/status' + window.location.search);
        const data = await res.json();
        state.keycloakUrl = data.keycloakUrl || '';
        state.realm = data.realm || 'idlistack';
        state.clientId = data.clientId || 'idlistack-cli';
        state.namespace = data.namespace || 'idlistack-app';
        state.projectOwner = data.projectOwner || '';
        document.getElementById('nsDisplay').textContent = state.namespace;

        // Project Ownership protection
        if (state.projectOwner && state.projectOwner !== 'existing-project') {
          const banner = document.getElementById('ownerLockBanner');
          document.getElementById('ownerName').textContent = state.projectOwner;
          banner.style.display = 'block';
          // Disable "Create User" for an already deployed project
          document.getElementById('tabRegister').style.opacity = '0.5';
          document.getElementById('tabRegister').title = 'Project is already owned by ' + state.projectOwner;
        }

        if (window.location.hash && window.location.hash.includes('access_token=')) {
          await finishSSO();
          return;
        }

        const params = new URLSearchParams(window.location.search);
        if (data.session && !params.get('login')) {
          showSignedIn(data.session);
        }
      } catch (e) {
        console.error(e);
      }
    }

    function showSignedIn(sess) {
      document.getElementById('authView').style.display = 'none';
      document.getElementById('successView').style.display = 'block';
      document.getElementById('okUser').textContent = sess.username;
      document.getElementById('okScope').textContent = sess.scope === 'cluster-wide' ? 'Cluster-Wide' : 'Namespace-Scoped';
      document.getElementById('okNs').textContent = sess.scope === 'cluster-wide'
        ? 'All Namespaces (*)'
        : ((sess.allowedNamespaces && sess.allowedNamespaces[0]) || state.namespace);
    }

    async function handleLogin(e) {
      e.preventDefault();
      const btn = document.getElementById('submitBtn');
      const errBox = document.getElementById('errorMsg');
      errBox.style.display = 'none';
      btn.disabled = true;
      btn.textContent = 'Signing in...';

      try {
        const res = await fetch('/api/auth/login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            username: document.getElementById('username').value.trim(),
            password: document.getElementById('password').value,
            namespace: state.namespace
          })
        });
        const data = await res.json();
        if (!data.ok) {
          errBox.textContent = data.error || 'Invalid username or password';
          errBox.style.display = 'block';
        } else {
          window.history.replaceState({}, document.title, '/');
          showSignedIn(data.session);
        }
      } catch (err) {
        errBox.textContent = 'Connection error: ' + err.message;
        errBox.style.display = 'block';
      } finally {
        btn.disabled = false;
        btn.textContent = 'Sign In';
      }
    }

    async function handleRegister(e) {
      e.preventDefault();
      const btn = document.getElementById('regBtn');
      const errBox = document.getElementById('errorMsg');
      errBox.style.display = 'none';
      btn.disabled = true;
      btn.textContent = 'Provisioning user & RBAC...';

      try {
        const res = await fetch('/api/auth/register', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            username: document.getElementById('regUsername').value.trim(),
            password: document.getElementById('regPassword').value,
            namespace: state.namespace
          })
        });
        const data = await res.json();
        if (!data.ok) {
          errBox.textContent = data.error || 'Could not create user';
          errBox.style.display = 'block';
        } else {
          window.history.replaceState({}, document.title, '/');
          showSignedIn(data.session);
        }
      } catch (err) {
        errBox.textContent = 'Connection error: ' + err.message;
        errBox.style.display = 'block';
      } finally {
        btn.disabled = false;
        btn.textContent = 'Create User & Sign In';
      }
    }

    function openKeycloakSSO() {
      const redirectUri = encodeURIComponent(window.location.origin + '/');
      window.location.href = state.keycloakUrl + '/realms/' + state.realm + '/protocol/openid-connect/auth?client_id=' + state.clientId + '&redirect_uri=' + redirectUri + '&response_type=token%20id_token&scope=openid%20profile%20email&nonce=' + Date.now();
    }

    async function finishSSO() {
      const hash = new URLSearchParams(window.location.hash.substring(1));
      const accessToken = hash.get('access_token');
      const idToken = hash.get('id_token') || '';
      const expiresIn = parseInt(hash.get('expires_in') || '86400', 10);
      window.history.replaceState({}, document.title, '/');
      if (!accessToken) return;

      const res = await fetch('/api/auth/token-exchange', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ accessToken, idToken, expiresIn, namespace: state.namespace })
      });
      const data = await res.json();
      if (data.ok) {
        showSignedIn(data.session);
      }
    }

    async function switchAccount() {
      await fetch('/api/auth/logout', { method: 'POST' });
      document.getElementById('password').value = '';
      document.getElementById('successView').style.display = 'none';
      document.getElementById('authView').style.display = 'block';
      setMode('login');
    }

    init();
  </script>
</body>
</html>
`
