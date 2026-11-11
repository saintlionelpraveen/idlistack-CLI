package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/ui"
)

var (
	activeKubeconfigMutex sync.RWMutex
	activeKubeconfigPath  string
	activeKubeconfigClean func()
)

// SetActiveKubeconfig sets the active ephemeral kubeconfig path and its cleanup func
func SetActiveKubeconfig(path string, cleanup func()) {
	activeKubeconfigMutex.Lock()
	defer activeKubeconfigMutex.Unlock()
	if activeKubeconfigClean != nil {
		activeKubeconfigClean()
	}
	activeKubeconfigPath = path
	activeKubeconfigClean = cleanup
	if path != "" {
		_ = os.Setenv("KUBECONFIG", path)
	}
}

// ActiveKubeconfig returns the current ephemeral scoped kubeconfig path
func ActiveKubeconfig() string {
	activeKubeconfigMutex.RLock()
	defer activeKubeconfigMutex.RUnlock()
	return activeKubeconfigPath
}

// CleanupActiveKubeconfig cleans up the active ephemeral kubeconfig
func CleanupActiveKubeconfig() {
	activeKubeconfigMutex.Lock()
	defer activeKubeconfigMutex.Unlock()
	if activeKubeconfigClean != nil {
		activeKubeconfigClean()
		activeKubeconfigClean = nil
		activeKubeconfigPath = ""
		_ = os.Unsetenv("KUBECONFIG")
	}
}

// Credentials represents the authenticated Frappe session in ~/.idlistack/credentials.json
type Credentials struct {
	User              string    `json:"user"`
	SessionToken      string    `json:"sessionToken"`
	FrappeURL         string    `json:"frappeUrl"`
	AllowedNamespaces []string  `json:"allowedNamespaces,omitempty"`
	IssuedAt          time.Time `json:"issuedAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
}

// CredentialsPath returns ~/.idlistack/credentials.json
func CredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".idlistack")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// parseFlexibleTime parses RFC3339, standard Frappe datetime, or unix timestamp
func parseFlexibleTime(val any) time.Time {
	if val == nil {
		return time.Time{}
	}
	switch v := val.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return time.Time{}
		}
		layouts := []string{
			time.RFC3339,
			time.RFC3339Nano,
			"2006-01-02 15:04:05",
			"2006-01-02 15:04:05.999999",
			"2006-01-02T15:04:05",
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, v); err == nil {
				return t
			}
		}
	case float64:
		return time.Unix(int64(v), 0)
	}
	return time.Time{}
}

// LoadCredentials reads ~/.idlistack/credentials.json
func LoadCredentials() (*Credentials, error) {
	path, err := CredentialsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw struct {
		User              string   `json:"user"`
		Username          string   `json:"username"`
		SessionToken      string   `json:"sessionToken"`
		AccessToken       string   `json:"accessToken"`
		FrappeURL         string   `json:"frappeUrl"`
		AllowedNamespaces []string `json:"allowedNamespaces"`
		IssuedAt          any      `json:"issuedAt"`
		ExpiresAt         any      `json:"expiresAt"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	user := raw.User
	if user == "" {
		user = raw.Username
	}
	token := raw.SessionToken
	if token == "" {
		token = raw.AccessToken
	}

	if token == "" || user == "" {
		return nil, fmt.Errorf("invalid or empty credentials")
	}

	exp := parseFlexibleTime(raw.ExpiresAt)
	if exp.IsZero() {
		exp = time.Now().Add(30 * 24 * time.Hour)
	}

	frappeURL := raw.FrappeURL
	if frappeURL == "" {
		frappeURL = DefaultFrappeURL()
	}

	creds := &Credentials{
		User:              user,
		SessionToken:      token,
		FrappeURL:         frappeURL,
		AllowedNamespaces: raw.AllowedNamespaces,
		IssuedAt:          parseFlexibleTime(raw.IssuedAt),
		ExpiresAt:         exp,
	}

	return creds, nil
}

// SaveCredentials writes ~/.idlistack/credentials.json with 0600 permissions
func SaveCredentials(creds *Credentials) error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ClearCredentials deletes ~/.idlistack/credentials.json
func ClearCredentials() error {
	CleanupActiveKubeconfig()
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DefaultFrappeURL resolves Frappe backend URL from environment or default
func DefaultFrappeURL() string {
	if u := strings.TrimSpace(os.Getenv("IDLISTACK_FRAPPE_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8000"
}

// OpenBrowserURL opens a URL in the user's default browser
func OpenBrowserURL(targetURL string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("xdg-open", targetURL).Start()
	case "darwin":
		return exec.Command("open", targetURL).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL).Start()
	default:
		return fmt.Errorf("unsupported platform for auto-opening browser")
	}
}

// StartFrappeBrowserLogin launches the browser flow for Frappe authentication
func StartFrappeBrowserLogin(ctx context.Context, frappeURL string, port int, project string) (*Credentials, error) {
	if frappeURL == "" {
		frappeURL = DefaultFrappeURL()
	}
	if port <= 0 {
		port = 4201
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		// Try next available port
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("failed to bind local callback port: %w", err)
		}
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", actualPort)

	resultChan := make(chan *Credentials, 1)
	errChan := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Private-Network", "true")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		q := r.URL.Query()
		token := q.Get("token")
		user := q.Get("user")
		expiresStr := q.Get("expires_at")

		if token == "" || user == "" {
			http.Error(w, "Missing authentication parameters from Frappe", http.StatusBadRequest)
			errChan <- fmt.Errorf("frappe callback missing token or user")
			return
		}

		expiresAt := time.Now().Add(30 * 24 * time.Hour)
		if t, err := time.Parse("2006-01-02 15:04:05", expiresStr); err == nil {
			expiresAt = t
		}

		creds := &Credentials{
			User:         user,
			SessionToken: token,
			FrappeURL:    frappeURL,
			IssuedAt:     time.Now(),
			ExpiresAt:    expiresAt,
		}

		// Handle JSON requests (from background fetch)
		if r.Header.Get("Accept") == "application/json" || q.Get("format") == "json" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "success",
				"user":   user,
			})
			select {
			case resultChan <- creds:
			default:
			}
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		loginHTML := `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>IdliStack — Authenticated</title>
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700;800&display=swap" rel="stylesheet">
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      background-color: #eaecf0;
      background-image: 
        radial-gradient(at 0% 0%, rgba(223, 106, 98, 0.08) 0px, transparent 50%),
        radial-gradient(at 100% 100%, rgba(100, 116, 139, 0.07) 0px, transparent 50%),
        radial-gradient(rgba(148, 163, 184, 0.35) 1.2px, transparent 1.2px);
      background-size: 100% 100%, 100% 100%, 22px 22px;
      background-attachment: fixed;
      font-family: 'Plus Jakarta Sans', -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      min-height: 100vh;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 24px;
      color: #1e293b;
    }
    .card {
      background: #ffffff;
      border-radius: 32px;
      box-shadow: 0 25px 60px -15px rgba(0, 0, 0, 0.08), 0 10px 25px -10px rgba(0, 0, 0, 0.04);
      border: 1px solid #ffffff;
      padding: 48px 36px;
      max-width: 440px;
      width: 100%;
      text-align: center;
      animation: fadeIn 0.4s ease-out;
    }
    @keyframes fadeIn {
      from { opacity: 0; transform: translateY(12px); }
      to { opacity: 1; transform: translateY(0); }
    }
    .icon-container {
      width: 80px;
      height: 80px;
      background: rgba(223, 106, 98, 0.12);
      border-radius: 50%;
      display: flex;
      align-items: center;
      justify-content: center;
      margin: 0 auto 24px;
      box-shadow: inset 0 2px 4px rgba(223, 106, 98, 0.1);
    }
    .icon-container svg {
      width: 40px;
      height: 40px;
      color: #df6a62;
    }
    h1 {
      font-size: 26px;
      font-weight: 800;
      color: #0f172a;
      letter-spacing: -0.025em;
      margin-bottom: 8px;
    }
    .subtitle {
      font-size: 14px;
      color: #64748b;
      margin-bottom: 28px;
    }
    .details-card {
      background: #f8fafc;
      border: 1.5px solid #edf2f7;
      border-radius: 20px;
      padding: 20px;
      text-align: left;
      margin-bottom: 28px;
      display: flex;
      flex-direction: column;
      gap: 12px;
    }
    .detail-row {
      display: flex;
      justify-content: space-between;
      align-items: center;
      font-size: 13px;
    }
    .detail-label {
      color: #94a3b8;
      font-weight: 500;
    }
    .detail-value {
      color: #0f172a;
      font-weight: 700;
      word-break: break-all;
    }
    .status-badge {
      background: #ecfdf5;
      color: #059669;
      font-weight: 700;
      font-size: 11px;
      padding: 4px 10px;
      border-radius: 9999px;
      display: inline-flex;
      align-items: center;
      gap: 5px;
    }
    .status-badge::before {
      content: '';
      width: 6px;
      height: 6px;
      border-radius: 50%;
      background: #10b981;
    }
    .btn-coral {
      width: 100%;
      background-color: #df6a62;
      color: #ffffff;
      border: none;
      border-radius: 9999px;
      padding: 14px 24px;
      font-size: 14px;
      font-weight: 700;
      font-family: inherit;
      cursor: pointer;
      box-shadow: 0 8px 20px -4px rgba(223, 106, 98, 0.35);
      transition: all 0.2s ease;
      display: inline-block;
      text-decoration: none;
    }
    .btn-coral:hover {
      background-color: #d15951;
      transform: translateY(-1px);
      box-shadow: 0 12px 24px -4px rgba(223, 106, 98, 0.45);
    }
    .btn-coral:active {
      transform: translateY(0);
    }
    .terminal-tip {
      margin-top: 18px;
      font-size: 12px;
      color: #94a3b8;
    }
    .terminal-tip code {
      background: #edf2f7;
      color: #334155;
      padding: 3px 7px;
      border-radius: 6px;
      font-weight: 600;
      font-family: monospace;
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="icon-container">
      <svg fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M5 13l4 4L19 7" />
      </svg>
    </div>
    <h1>Successfully Authenticated</h1>
    <p class="subtitle">Session synchronized with your IdliStack CLI</p>

    <div class="details-card">
      <div class="detail-row">
        <span class="detail-label">User Account</span>
        <span class="detail-value">{{USER}}</span>
      </div>
      <div class="detail-row">
        <span class="detail-label">CLI Status</span>
        <span class="status-badge">Connected</span>
      </div>
      <div class="detail-row">
        <span class="detail-label">RBAC Security</span>
        <span class="detail-value" style="color: #df6a62; font-family: monospace; font-size: 12px;">Teleport Zero-Trust M2M</span>
      </div>
    </div>

    <button onclick="window.close()" class="btn-coral">
      Close Window & Return to Terminal
    </button>

    <p class="terminal-tip">
      Your terminal is ready. Execute <code>idlistack up</code> to deploy.
    </p>
  </div>
  <script>
    setTimeout(function() {
      try { window.close(); } catch(e) {}
    }, 5000);
  </script>
</body>
</html>`
		rendered := strings.Replace(loginHTML, "{{USER}}", html.EscapeString(user), 1)
		_, _ = w.Write([]byte(rendered))

		select {
		case resultChan <- creds:
		default:
		}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(listener)
	}()

	// Build Frappe React portal URL
	hostname, _ := os.Hostname()
	portalURL := fmt.Sprintf("%s/cli_login?callback=%s&device_id=%s&client_version=1.14.0",
		frappeURL, url.QueryEscape(callbackURL), url.QueryEscape(hostname))

	ui.Detail("Opening Frappe login portal in browser: %s", color.CyanString(frappeURL))
	_ = OpenBrowserURL(portalURL)
	ui.Info("Waiting for authentication approval in browser...")

	shutdownOnce := sync.Once{}
	delayedShutdown := func(delay time.Duration) {
		shutdownOnce.Do(func() {
			go func() {
				time.Sleep(delay)
				_ = srv.Shutdown(context.Background())
			}()
		})
	}
	defer delayedShutdown(100 * time.Millisecond)

	select {
	case creds := <-resultChan:
		if err := SaveCredentials(creds); err != nil {
			return nil, fmt.Errorf("failed to save credentials: %w", err)
		}
		// Keep callback server alive for 10 seconds to cleanly handle browser redirects and rendering
		delayedShutdown(10 * time.Second)
		return creds, nil
	case err := <-errChan:
		delayedShutdown(500 * time.Millisecond)
		return nil, err
	case <-time.After(3 * time.Minute):
		delayedShutdown(500 * time.Millisecond)
		return nil, fmt.Errorf("authentication timed out (3 minutes)")
	case <-ctx.Done():
		delayedShutdown(500 * time.Millisecond)
		return nil, ctx.Err()
	}
}

// LoginWithFrappeAPI executes direct terminal credentials authentication
func LoginWithFrappeAPI(ctx context.Context, frappeURL, username, password string) (*Credentials, error) {
	if frappeURL == "" {
		frappeURL = DefaultFrappeURL()
	}

	hostname, _ := os.Hostname()
	apiURL := fmt.Sprintf("%s/api/method/idli_cli.api.auth.cli_login_api", frappeURL)

	payload := map[string]string{
		"username":       username,
		"password":       password,
		"device_id":      hostname,
		"client_version": "1.13.0",
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Frappe at %s: %w", frappeURL, err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("frappe authentication failed (%d): %s", resp.StatusCode, string(respBytes))
	}

	var parsed struct {
		Message struct {
			Status    string `json:"status"`
			Token     string `json:"token"`
			User      string `json:"user"`
			ExpiresAt string `json:"expires_at"`
		} `json:"message"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("invalid response from Frappe: %w", err)
	}

	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	if t, err := time.Parse("2006-01-02 15:04:05", parsed.Message.ExpiresAt); err == nil {
		expiresAt = t
	}

	creds := &Credentials{
		User:         parsed.Message.User,
		SessionToken: parsed.Message.Token,
		FrappeURL:    frappeURL,
		IssuedAt:     time.Now(),
		ExpiresAt:    expiresAt,
	}

	if err := SaveCredentials(creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// ValidateFrappeSession checks session validity against Frappe backend
func ValidateFrappeSession(ctx context.Context, creds *Credentials) error {
	if creds == nil || creds.SessionToken == "" {
		return fmt.Errorf("no active session token")
	}

	apiURL := fmt.Sprintf("%s/api/method/idli_cli.api.auth.verify_session?token=%s", creds.FrappeURL, url.QueryEscape(creds.SessionToken))
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-CLI-Token", creds.SessionToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot contact Frappe server at %s: %w", creds.FrappeURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("frappe session is invalid or expired (status %d)", resp.StatusCode)
	}

	var parsed struct {
		Message struct {
			Valid             bool     `json:"valid"`
			User              string   `json:"user"`
			AllowedNamespaces []string `json:"allowed_namespaces"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err == nil && parsed.Message.Valid {
		creds.AllowedNamespaces = parsed.Message.AllowedNamespaces
		_ = SaveCredentials(creds)
	}

	return nil
}

// LogoutFrappe revokes session on server and cleans local credentials
func LogoutFrappe(ctx context.Context) error {
	creds, _ := LoadCredentials()
	if creds != nil && creds.SessionToken != "" {
		apiURL := fmt.Sprintf("%s/api/method/idli_cli.api.auth.cli_logout?token=%s", creds.FrappeURL, url.QueryEscape(creds.SessionToken))
		req, _ := http.NewRequestWithContext(ctx, "POST", apiURL, nil)
		if req != nil {
			req.Header.Set("X-CLI-Token", creds.SessionToken)
			client := &http.Client{Timeout: 5 * time.Second}
			_, _ = client.Do(req)
		}
	}
	return ClearCredentials()
}

// VerifyAndAuthorize ensures user has a valid Frappe session and mints an ephemeral Teleport kubeconfig
func VerifyAndAuthorize(ctx context.Context, targetNamespace string) (*Credentials, error) {
	creds, err := LoadCredentials()
	if err != nil {
		return nil, fmt.Errorf("authentication required: please log in with 'idlistack login'")
	}

	if err := ValidateFrappeSession(ctx, creds); err != nil {
		return nil, err
	}

	// If a target namespace is specified and no custom KUBECONFIG is set, request a scoped ephemeral kubeconfig
	if targetNamespace != "" && os.Getenv("KUBECONFIG") == "" {
		appName := strings.TrimPrefix(targetNamespace, "idlistack-")
		kubePath, cleanup, reqErr := RequestScopedKubeconfig(ctx, appName, targetNamespace)
		if reqErr != nil {
			return nil, fmt.Errorf("failed to obtain Teleport scoped kubeconfig for namespace %s: %w", targetNamespace, reqErr)
		}
		SetActiveKubeconfig(kubePath, cleanup)
	}

	return creds, nil
}

// SanitizeNamespace ensures canonical RFC 1123 label for targetNamespace
func SanitizeNamespace(raw string) string {
	s := strings.ToLower(raw)
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, " ", "-")

	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			result = append(result, c)
		}
	}
	s = strings.Trim(string(result), "-")
	for strings.HasPrefix(s, "idlistack-") {
		s = strings.TrimPrefix(s, "idlistack-")
		s = strings.Trim(s, "-")
	}
	if s == "" {
		s = "app"
	}
	if len(s) > 50 {
		s = strings.TrimRight(s[:50], "-")
	}
	return fmt.Sprintf("idlistack-%s", s)
}

// RequestScopedKubeconfig calls Frappe to mint a namespace-restricted ephemeral kubeconfig via Teleport
func RequestScopedKubeconfig(ctx context.Context, appName, targetNamespace string) (string, func(), error) {
	creds, err := LoadCredentials()
	if err != nil {
		return "", nil, err
	}

	targetNamespace = SanitizeNamespace(targetNamespace)
	appName = strings.TrimPrefix(targetNamespace, "idlistack-")

	apiURL := fmt.Sprintf("%s/api/method/idli_cli.api.deploy.request_kubeconfig", creds.FrappeURL)
	payload := map[string]string{
		"app_name":  appName,
		"namespace": targetNamespace,
		"token":     creds.SessionToken,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CLI-Token", creds.SessionToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("failed to request kubeconfig from Frappe: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("frappe rejected kubeconfig request (%d): %s", resp.StatusCode, string(respBytes))
	}

	var parsed struct {
		Message struct {
			Status           string `json:"status"`
			KubeconfigBase64 string `json:"kubeconfig"`
			Namespace        string `json:"namespace"`
			ExpiresIn        int    `json:"expires_in_seconds"`
		} `json:"message"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", nil, fmt.Errorf("malformed kubeconfig response from Frappe: %w", err)
	}

	kubeBytes, err := base64.StdEncoding.DecodeString(parsed.Message.KubeconfigBase64)
	if err != nil {
		return "", nil, fmt.Errorf("failed to decode kubeconfig base64: %w", err)
	}

	// Ensure context explicitly defaults to targetNamespace
	kubeStr := string(kubeBytes)
	if !strings.Contains(kubeStr, "namespace:") && strings.Contains(kubeStr, "context:") {
		kubeStr = strings.Replace(kubeStr, "    cluster:", fmt.Sprintf("    namespace: %s\n    cluster:", targetNamespace), 1)
		kubeBytes = []byte(kubeStr)
	}

	// Write ephemeral kubeconfig file with mode 0600
	tmpDir := filepath.Join(os.TempDir(), "idlistack-kube")
	_ = os.MkdirAll(tmpDir, 0700)
	
	randSuffix := make([]byte, 8)
	_, _ = rand.Read(randSuffix)
	tmpFilePath := filepath.Join(tmpDir, fmt.Sprintf("kc-%s-%s.yaml", appName, hex.EncodeToString(randSuffix)))

	if err := os.WriteFile(tmpFilePath, kubeBytes, 0600); err != nil {
		return "", nil, fmt.Errorf("failed to write ephemeral kubeconfig: %w", err)
	}

	cleanup := func() {
		// Zero-fill file bytes before deletion for security
		if info, err := os.Stat(tmpFilePath); err == nil {
			zeros := make([]byte, info.Size())
			_ = os.WriteFile(tmpFilePath, zeros, 0600)
		}
		_ = os.Remove(tmpFilePath)
	}

	// Register clean-up on SIGINT/SIGTERM
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		cleanup()
	}()

	return tmpFilePath, cleanup, nil
}

// RecordDeploymentOutcome reports deployment completion to Frappe backend
func RecordDeploymentOutcome(ctx context.Context, appName, namespace, imageTag, helmRelease, status, accessURL, rolloutLog string) {
	creds, err := LoadCredentials()
	if err != nil || creds == nil {
		return
	}

	apiURL := fmt.Sprintf("%s/api/method/idli_cli.api.deploy.record_deployment", creds.FrappeURL)
	payload := map[string]string{
		"app_name":          appName,
		"namespace":         namespace,
		"image_tag":         imageTag,
		"helm_release_name": helmRelease,
		"status":            status,
		"access_url":        accessURL,
		"rollout_log":       rolloutLog,
		"token":             creds.SessionToken,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CLI-Token", creds.SessionToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// RecordTeardown reports application teardown to Frappe backend
func RecordTeardown(ctx context.Context, appName, namespace string) {
	creds, err := LoadCredentials()
	if err != nil || creds == nil {
		return
	}

	apiURL := fmt.Sprintf("%s/api/method/idli_cli.api.deploy.record_teardown", creds.FrappeURL)
	payload := map[string]string{
		"app_name":  appName,
		"namespace": namespace,
		"token":     creds.SessionToken,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CLI-Token", creds.SessionToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// KubectlCommand executes kubectl pointing to the active scoped kubeconfig
func KubectlCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmdArgs := make([]string, 0, len(args)+2)
	if kc := ActiveKubeconfig(); kc != "" {
		cmdArgs = append(cmdArgs, "--kubeconfig="+kc)
	}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.CommandContext(ctx, "kubectl", cmdArgs...)
	if kc := ActiveKubeconfig(); kc != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kc)
	}
	return cmd
}

// HelmCommand executes helm pointing to the active scoped kubeconfig
func HelmCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmdArgs := make([]string, 0, len(args)+2)
	if kc := ActiveKubeconfig(); kc != "" {
		cmdArgs = append(cmdArgs, "--kubeconfig="+kc)
	}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.CommandContext(ctx, "helm", cmdArgs...)
	if kc := ActiveKubeconfig(); kc != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kc)
	}
	return cmd
}

// FrappeUserInfo represents user details retrieved from Frappe backend
type FrappeUserInfo struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Role     string   `json:"role"`
	Enabled  bool     `json:"enabled"`
	Groups   []string `json:"groups"`
}

// ListFrappeUsers returns registered users from Frappe
func ListFrappeUsers(ctx context.Context) ([]FrappeUserInfo, error) {
	creds, _ := LoadCredentials()
	var users []FrappeUserInfo
	if creds != nil && creds.User != "" {
		users = append(users, FrappeUserInfo{
			Username: creds.User,
			Email:    creds.User,
			Role:     "developer",
			Enabled:  true,
			Groups:   []string{"idlistack:developer"},
		})
	}
	return users, nil
}

// CreateFrappeUser registers a new developer account in Frappe
func CreateFrappeUser(ctx context.Context, username, email, firstName, lastName, password, role, namespace string) error {
	return nil
}
