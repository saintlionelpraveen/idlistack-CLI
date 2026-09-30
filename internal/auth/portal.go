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
	"runtime"
	"strings"
	"time"

	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/k8s"
)

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
		Handler:      mux,
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
	kcRunning, kcURL := k8s.IsKeycloakRunning(ctx)
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
		"namespace":       "idlistack-" + strings.ToLower(project),
		"session":         activeSession,
	})
}

func (ps *PortalServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		Namespace string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid request"})
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

	creds, err := LoginWithKeycloak(ctx, kcURL, k8s.KeycloakRealm, k8s.KeycloakClientID, strings.TrimSpace(req.Username), req.Password, allowedNS)
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

	var req struct {
		Username  string `json:"username"`
		Email     string `json:"email"`
		Password  string `json:"password"`
		Role      string `json:"role"`
		Namespace string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid request"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	ns := strings.TrimSpace(req.Namespace)
	if ns == "" && ps.defaultProject != "" {
		ns = "idlistack-" + strings.ToLower(ps.defaultProject)
	}

	creds, err := RegisterAndLoginKeycloakUser(ctx, req.Username, req.Email, req.Password, req.Role, ns)
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
  <title>Sign in — IdliStack</title>
  <style>
    :root {
      --bg: #090d16;
      --card: #111827;
      --border: #1f2937;
      --text: #f9fafb;
      --muted: #9ca3af;
      --primary: #e152c1;
      --cyan: #06b6d4;
      --green: #10b981;
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
      max-width: 400px;
      background: var(--card);
      border: 1px solid var(--border);
      border-radius: 16px;
      padding: 30px 26px;
      box-shadow: 0 20px 40px rgba(0, 0, 0, 0.45);
    }
    .logo {
      width: 42px;
      height: 42px;
      border-radius: 10px;
      background: linear-gradient(135deg, var(--primary), var(--cyan));
      display: flex;
      align-items: center;
      justify-content: center;
      font-weight: 800;
      font-size: 20px;
      color: #fff;
      margin: 0 auto 14px;
    }
    h1 { font-size: 20px; font-weight: 700; text-align: center; margin-bottom: 4px; }
    .subtitle { font-size: 13px; color: var(--muted); text-align: center; margin-bottom: 18px; }
    .tabs {
      display: grid;
      grid-template-columns: 1fr 1fr;
      background: #0b0f19;
      border: 1px solid var(--border);
      border-radius: 9px;
      padding: 3px;
      margin-bottom: 18px;
    }
    .tab {
      padding: 7px;
      border: none;
      background: transparent;
      color: var(--muted);
      font-size: 13px;
      font-weight: 600;
      border-radius: 6px;
      cursor: pointer;
    }
    .tab.active { background: #1f2937; color: var(--text); }
    .ns-pill {
      display: flex;
      align-items: center;
      justify-content: space-between;
      background: #0b0f19;
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 8px 12px;
      font-size: 12px;
      margin-bottom: 16px;
    }
    .ns-pill span:first-child { color: var(--muted); }
    .ns-pill code { color: var(--cyan); font-weight: 600; }
    .field { margin-bottom: 13px; }
    label { display: block; font-size: 12px; font-weight: 500; color: var(--muted); margin-bottom: 5px; }
    input, select {
      width: 100%;
      padding: 10px 12px;
      background: #0b0f19;
      border: 1px solid #374151;
      border-radius: 8px;
      color: var(--text);
      font-size: 14px;
      outline: none;
    }
    input:focus, select:focus { border-color: var(--cyan); }
    .btn {
      width: 100%;
      padding: 11px 14px;
      border-radius: 8px;
      border: none;
      font-size: 14px;
      font-weight: 600;
      cursor: pointer;
    }
    .btn:disabled { opacity: 0.6; cursor: not-allowed; }
    .btn-primary { background: linear-gradient(135deg, var(--primary), var(--cyan)); color: #fff; margin-top: 4px; }
    .btn-outline { background: transparent; color: var(--muted); border: 1px solid var(--border); margin-top: 10px; font-size: 13px; }
    .btn-outline:hover { color: var(--text); border-color: #374151; }
    .msg {
      margin-top: 14px;
      padding: 10px 12px;
      border-radius: 8px;
      font-size: 13px;
      text-align: center;
      display: none;
    }
    .msg-error { background: rgba(239, 68, 68, 0.12); border: 1px solid rgba(239, 68, 68, 0.35); color: #fca5a5; }
    .success-icon {
      width: 48px;
      height: 48px;
      border-radius: 50%;
      background: rgba(16, 185, 129, 0.15);
      border: 1px solid rgba(16, 185, 129, 0.4);
      color: #34d399;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 22px;
      margin: 0 auto 14px;
    }
    .info-box {
      background: #0b0f19;
      border: 1px solid var(--border);
      border-radius: 10px;
      padding: 14px;
      margin: 18px 0;
    }
    .info-row { display: flex; justify-content: space-between; font-size: 13px; padding: 6px 0; }
    .info-row span:first-child { color: var(--muted); }
    .info-row span:last-child { font-weight: 600; color: var(--text); }
    .hint { font-size: 12px; color: var(--muted); text-align: center; line-height: 1.5; margin-bottom: 14px; }
  </style>
</head>
<body>
  <div class="card">
    <div id="authView">
      <div class="logo">I</div>
      <h1>IdliStack Auth</h1>
      <p class="subtitle">Dynamic K3s Keycloak RBAC</p>

      <div class="tabs">
        <button type="button" class="tab active" id="tabLogin" onclick="setMode('login')">Sign In</button>
        <button type="button" class="tab" id="tabRegister" onclick="setMode('register')">Create User</button>
      </div>

      <div class="ns-pill">
        <span>Target Namespace</span>
        <code id="nsDisplay">idlistack-app</code>
      </div>

      <!-- Sign In Form -->
      <form id="loginForm" onsubmit="handleLogin(event)">
        <div class="field">
          <label>Username</label>
          <input type="text" id="username" placeholder="Enter username" required />
        </div>
        <div class="field">
          <label>Password</label>
          <input type="password" id="password" placeholder="••••••••" required />
        </div>
        <button type="submit" class="btn btn-primary" id="submitBtn">Sign In</button>
      </form>

      <!-- Dynamic Create User Form -->
      <form id="registerForm" style="display:none;" onsubmit="handleRegister(event)">
        <div class="field">
          <label>New Username</label>
          <input type="text" id="regUsername" placeholder="e.g. praveen" required />
        </div>
        <div class="field">
          <label>Password</label>
          <input type="password" id="regPassword" placeholder="Create password" required />
        </div>
        <div class="field">
          <label>K3s Access Level</label>
          <select id="regRole">
            <option value="developer">Namespace-Scoped (This Project Only)</option>
            <option value="admin">Cluster-Wide Admin (All Namespaces)</option>
          </select>
        </div>
        <button type="submit" class="btn btn-primary" id="regBtn">Create User & Sign In</button>
      </form>

      <button type="button" class="btn btn-outline" onclick="openKeycloakSSO()">
        Continue with Keycloak SSO ↗
      </button>

      <div id="errorMsg" class="msg msg-error"></div>
    </div>

    <!-- Signed-In Confirmation -->
    <div id="successView" style="display:none;">
      <div class="success-icon">✓</div>
      <h1>Authenticated</h1>
      <p class="subtitle">Your K3s RBAC session is active</p>

      <div class="info-box">
        <div class="info-row">
          <span>User</span>
          <span id="okUser">-</span>
        </div>
        <div class="info-row">
          <span>RBAC Scope</span>
          <span id="okScope" style="color:#34d399;">-</span>
        </div>
        <div class="info-row">
          <span>Namespace</span>
          <span id="okNs" style="color:#22d3ee; font-family:monospace;">-</span>
        </div>
      </div>

      <p class="hint">You can close this tab and return to your terminal or VS Code.</p>

      <button type="button" class="btn btn-outline" onclick="switchAccount()">
        Switch Account / Create New User
      </button>
    </div>
  </div>

  <script>
    let state = { keycloakUrl: '', realm: 'idlistack', clientId: 'idlistack-cli', namespace: 'idlistack-app' };

    function setMode(mode) {
      document.getElementById('errorMsg').style.display = 'none';
      if (mode === 'register') {
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
        document.getElementById('nsDisplay').textContent = state.namespace;

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
      btn.textContent = 'Creating user & K3s RBAC...';

      try {
        const res = await fetch('/api/auth/register', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            username: document.getElementById('regUsername').value.trim(),
            password: document.getElementById('regPassword').value,
            role: document.getElementById('regRole').value,
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
    }

    init();
  </script>
</body>
</html>
`
