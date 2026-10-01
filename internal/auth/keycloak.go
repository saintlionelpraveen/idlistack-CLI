package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/idlistack/cli/internal/k8s"
)

// Credentials represents the saved Keycloak OIDC & K3s RBAC session in ~/.idlistack/credentials.json
type Credentials struct {
	Username          string    `json:"username"`
	Email             string    `json:"email,omitempty"`
	Subject           string    `json:"subject,omitempty"`
	Groups            []string  `json:"groups"`
	Scope             string    `json:"scope"` // "cluster-wide" or "namespace-scoped"
	AllowedNamespaces []string  `json:"allowedNamespaces,omitempty"`
	AccessToken       string    `json:"accessToken"`
	IDToken           string    `json:"idToken,omitempty"`
	RefreshToken      string    `json:"refreshToken,omitempty"`
	K8sToken          string    `json:"k8sToken,omitempty"`
	KeycloakURL       string    `json:"keycloakUrl"`
	Realm             string    `json:"realm"`
	ClientID          string    `json:"clientId"`
	IssuedAt          time.Time `json:"issuedAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
}

type oidcTokenResponse struct {
	AccessToken      string `json:"access_token"`
	IDToken          string `json:"id_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
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
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
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
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoginWithKeycloak authenticates against the Keycloak OIDC token endpoint in K3s,
// extracts RBAC groups/claims from the JWT, provisions K3s RBAC bindings, and saves credentials.
func LoginWithKeycloak(ctx context.Context, keycloakURL, realm, clientID, username, password string, allowedNamespaces []string) (*Credentials, error) {
	if keycloakURL == "" {
		keycloakURL = k8s.GetKeycloakBaseURL(ctx)
	}
	keycloakURL = strings.TrimSuffix(keycloakURL, "/")
	if realm == "" {
		realm = k8s.KeycloakRealm
	}
	if clientID == "" {
		clientID = k8s.KeycloakClientID
	}

	tokenEndpoint := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", keycloakURL, realm)

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "password")
	form.Set("username", username)
	form.Set("password", password)
	form.Set("scope", "openid profile email")

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Keycloak in K3s at %s (run 'idlistack auth setup' to deploy Keycloak to K3s): %w", keycloakURL, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var tokResp oidcTokenResponse
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return nil, fmt.Errorf("invalid response from Keycloak (HTTP %d): %s", resp.StatusCode, string(body))
	}

	if resp.StatusCode != http.StatusOK || tokResp.AccessToken == "" {
		errMsg := tokResp.ErrorDescription
		if errMsg == "" {
			errMsg = tokResp.Error
		}
		if errMsg == "" {
			errMsg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("Keycloak authentication failed: %s", errMsg)
	}

	return BuildAndSaveCredentialsFromTokens(ctx, keycloakURL, realm, clientID, tokResp.AccessToken, tokResp.IDToken, tokResp.RefreshToken, tokResp.ExpiresIn, allowedNamespaces)
}

// BuildAndSaveCredentialsFromTokens decodes the Keycloak JWT claims, determines Cluster-Wide vs Namespace-Scoped RBAC,
// provisions the K3s RBAC bindings, and saves the credentials file.
func BuildAndSaveCredentialsFromTokens(ctx context.Context, keycloakURL, realm, clientID, accessToken, idToken, refreshToken string, expiresIn int, allowedNamespaces []string) (*Credentials, error) {
	jwtToParse := idToken
	if jwtToParse == "" {
		jwtToParse = accessToken
	}

	claims := decodeJWTClaims(jwtToParse)
	accessClaims := decodeJWTClaims(accessToken)

	username, _ := claims["preferred_username"].(string)
	if username == "" {
		username, _ = accessClaims["preferred_username"].(string)
	}
	if username == "" {
		username = "user"
	}

	email, _ := claims["email"].(string)
	sub, _ := claims["sub"].(string)

	groups := extractStringSlice(claims["groups"])
	if len(groups) == 0 {
		groups = extractStringSlice(accessClaims["groups"])
	}

	// Also extract realm_access.roles if present
	if ra, ok := accessClaims["realm_access"].(map[string]any); ok {
		for _, r := range extractStringSlice(ra["roles"]) {
			if r == "cluster-admins" || r == "developers" || strings.HasPrefix(r, "ns:") {
				groups = appendUnique(groups, r)
			}
		}
	}

	scope := "namespace-scoped"
	var tokenNamespaces []string
	for _, g := range groups {
		gClean := strings.ToLower(strings.TrimPrefix(g, "/"))
		if gClean == "cluster-admins" || gClean == "admin" || gClean == "admins" {
			scope = "cluster-wide"
		}
		if strings.HasPrefix(gClean, "ns:") {
			ns := strings.TrimPrefix(gClean, "ns:")
			if !strings.HasPrefix(ns, "idlistack-") && ns != "*" {
				ns = "idlistack-" + ns
			}
			tokenNamespaces = appendUnique(tokenNamespaces, ns)
		}
	}

	if scope == "cluster-wide" {
		allowedNamespaces = []string{"*"}
	} else if len(tokenNamespaces) > 0 {
		// Explicit ns:<namespace> groups in Keycloak define the user's dynamic RBAC scope
		allowedNamespaces = tokenNamespaces
	}

	if expiresIn <= 0 {
		expiresIn = 86400
	}

	targetNS := ""
	if len(allowedNamespaces) > 0 && allowedNamespaces[0] != "*" {
		for _, ns := range allowedNamespaces {
			if !strings.HasPrefix(ns, "idlistack-") {
				ns = "idlistack-" + ns
			}
			targetNS = ns
			_, _ = k8s.EnsureNamespaceRBAC(ctx, username, groups, allowedNamespaces, ns)
		}
	}

	k8sToken, _ := k8s.EnsureNamespaceRBAC(ctx, username, groups, allowedNamespaces, targetNS)

	creds := &Credentials{
		Username:          username,
		Email:             email,
		Subject:           sub,
		Groups:            groups,
		Scope:             scope,
		AllowedNamespaces: allowedNamespaces,
		AccessToken:       accessToken,
		IDToken:           idToken,
		RefreshToken:      refreshToken,
		K8sToken:          k8sToken,
		KeycloakURL:       keycloakURL,
		Realm:             realm,
		ClientID:          clientID,
		IssuedAt:          time.Now(),
		ExpiresAt:         time.Now().Add(time.Duration(expiresIn) * time.Second),
	}

	if err := SaveCredentials(creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// RefreshSession refreshes an expired token using the Keycloak refresh_token
func RefreshSession(ctx context.Context, creds *Credentials) (*Credentials, error) {
	if creds.RefreshToken == "" || creds.KeycloakURL == "" {
		return nil, fmt.Errorf("session expired; please run 'idlistack login'")
	}

	tokenEndpoint := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", strings.TrimSuffix(creds.KeycloakURL, "/"), creds.Realm)
	form := url.Values{}
	form.Set("client_id", creds.ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", creds.RefreshToken)

	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh Keycloak token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Keycloak session expired (HTTP %d); please run 'idlistack login'", resp.StatusCode)
	}

	var tokResp oidcTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil || tokResp.AccessToken == "" {
		return nil, fmt.Errorf("invalid refresh response from Keycloak")
	}

	return BuildAndSaveCredentialsFromTokens(ctx, creds.KeycloakURL, creds.Realm, creds.ClientID, tokResp.AccessToken, tokResp.IDToken, tokResp.RefreshToken, tokResp.ExpiresIn, creds.AllowedNamespaces)
}

// ValidateKeycloakSession checks if the user still exists and their token is still valid with Keycloak.
// If the user was deleted, disabled, or session revoked in Keycloak, it clears the local credentials
// and returns an error forcing re-authentication.
func ValidateKeycloakSession(ctx context.Context, creds *Credentials) error {
	if creds == nil || creds.AccessToken == "" || creds.KeycloakURL == "" {
		return fmt.Errorf("no active session")
	}

	kcURL := strings.TrimSuffix(creds.KeycloakURL, "/")
	userInfoURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/userinfo", kcURL, creds.Realm)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Keycloak may be briefly unreachable; if local expiration is still valid, don't hard-fail on network glitch
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		// Attempt token refresh first if refresh token is available
		if creds.RefreshToken != "" {
			if refreshed, refErr := RefreshSession(ctx, creds); refErr == nil && refreshed != nil {
				*creds = *refreshed
				return nil
			}
		}
		// Token rejected and refresh failed -> user deleted or revoked in Keycloak!
		_ = ClearCredentials()
		return fmt.Errorf("user %q was deleted or session was revoked in Keycloak; please run 'idlistack login'", creds.Username)
	}

	return nil
}

// UserExistsInKeycloak checks if a specific username exists in the Keycloak idlistack realm.
func UserExistsInKeycloak(ctx context.Context, username string) bool {
	username = strings.TrimSpace(username)
	if username == "" {
		return false
	}
	kcURL := k8s.GetKeycloakBaseURL(ctx)
	adminUser, adminPass := k8s.GetKeycloakAdminCredentials(ctx)
	if adminUser == "" {
		adminUser = "admin"
	}
	if adminPass == "" {
		adminPass = "admin"
	}

	adminTokenURL := fmt.Sprintf("%s/realms/master/protocol/openid-connect/token", kcURL)
	form := url.Values{}
	form.Set("client_id", "admin-cli")
	form.Set("grant_type", "password")
	form.Set("username", adminUser)
	form.Set("password", adminPass)

	client := &http.Client{Timeout: 4 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adminTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	var adminTok oidcTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&adminTok); err != nil || adminTok.AccessToken == "" {
		return false
	}

	return findKeycloakUserID(ctx, client, kcURL, adminTok.AccessToken, username) != ""
}

// VerifyAndAuthorize checks that a user is logged in via Keycloak, actively validates that the user
// still exists in Keycloak (has not been deleted), refreshes their token if expired,
// and verifies RBAC authorization for the target namespace (if provided).
func VerifyAndAuthorize(ctx context.Context, targetNamespace string) (*Credentials, error) {
	creds, err := LoadCredentials()
	if err != nil {
		return nil, fmt.Errorf("authentication required: please log in first with 'idlistack login'")
	}

	// Active Keycloak validation: verify user was not deleted or revoked in Keycloak
	if valErr := ValidateKeycloakSession(ctx, creds); valErr != nil {
		return nil, valErr
	}

	if time.Now().After(creds.ExpiresAt) {
		refreshed, err := RefreshSession(ctx, creds)
		if err != nil {
			return nil, err
		}
		creds = refreshed
	}

	// Ensure RBAC bindings exist for targetNamespace and verify namespace permission
	if targetNamespace != "" {
		k8sToken, rbacErr := k8s.EnsureNamespaceRBAC(ctx, creds.Username, creds.Groups, creds.AllowedNamespaces, targetNamespace)
		if rbacErr != nil {
			return nil, rbacErr
		}
		if k8sToken != "" && k8sToken != creds.K8sToken {
			creds.K8sToken = k8sToken
			_ = SaveCredentials(creds)
		}
	}

	return creds, nil
}

// ActiveKubeToken returns the token to pass to kubectl/helm (--token / --kube-token)
func ActiveKubeToken() string {
	creds, err := LoadCredentials()
	if err != nil || creds == nil {
		return ""
	}
	if creds.K8sToken != "" {
		return creds.K8sToken
	}
	return ""
}

// KubectlCommand wraps exec.CommandContext("kubectl", ...) and automatically injects --token
// when a scoped RBAC token is present.
func KubectlCommand(ctx context.Context, args ...string) *exec.Cmd {
	if tok := ActiveKubeToken(); tok != "" {
		args = append(args, "--token="+tok)
	}
	return exec.CommandContext(ctx, "kubectl", args...)
}

// HelmCommand wraps exec.CommandContext("helm", ...) and automatically injects --kube-token
// when a scoped RBAC token is present.
func HelmCommand(ctx context.Context, args ...string) *exec.Cmd {
	if tok := ActiveKubeToken(); tok != "" {
		args = append(args, "--kube-token="+tok)
	}
	return exec.CommandContext(ctx, "helm", args...)
}

func decodeJWTClaims(jwtStr string) map[string]any {
	parts := strings.Split(jwtStr, ".")
	if len(parts) < 2 {
		return map[string]any{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return map[string]any{}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return map[string]any{}
	}
	return claims
}

func extractStringSlice(v any) []string {
	var out []string
	if arr, ok := v.([]any); ok {
		for _, item := range arr {
			if s, ok := item.(string); ok {
				out = append(out, strings.TrimPrefix(s, "/"))
			}
		}
	}
	return out
}

func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

// CreateKeycloakUser dynamically creates a new user in Keycloak (`idlistack` realm),
// assigns them to the requested group (`cluster-admins` or `developers` + `ns:<namespace>`),
// and provisions their K3s RBAC bindings automatically.
func CreateKeycloakUser(ctx context.Context, username, email, password, role, targetNamespace string) error {
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	if username == "" || password == "" {
		return fmt.Errorf("username and password are required")
	}
	if email == "" {
		email = fmt.Sprintf("%s@%s.local", strings.ToLower(username), k8s.KeycloakRealm)
	}

	kcURL := k8s.GetKeycloakBaseURL(ctx)
	client := &http.Client{Timeout: 10 * time.Second}

	// 1. Obtain Keycloak Master Admin Token dynamically from cluster secret or environment
	adminUser, adminPass := k8s.GetKeycloakAdminCredentials(ctx)
	if adminUser == "" {
		adminUser = "admin"
	}
	if adminPass == "" {
		adminPass = "admin"
	}
	adminTokenURL := fmt.Sprintf("%s/realms/master/protocol/openid-connect/token", kcURL)
	form := url.Values{}
	form.Set("client_id", "admin-cli")
	form.Set("grant_type", "password")
	form.Set("username", adminUser)
	form.Set("password", adminPass)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adminTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to Keycloak in K3s (%s): %w", kcURL, err)
	}
	defer resp.Body.Close()

	var adminTok oidcTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&adminTok); err != nil || adminTok.AccessToken == "" {
		return fmt.Errorf("failed to authenticate with Keycloak Admin API (HTTP %d)", resp.StatusCode)
	}

	// 2. Ensure self-registration is enabled on the idlistack realm
	enableRealmSelfRegistration(ctx, client, kcURL, adminTok.AccessToken)

	// 3. Determine dynamic groups based on role and targetNamespace
	isClusterAdmin := strings.EqualFold(role, "admin") || strings.EqualFold(role, "cluster-wide") || strings.EqualFold(role, "cluster-admin")
	groups := []string{"developers"}
	var allowedNS []string

	if isClusterAdmin {
		groups = []string{"cluster-admins"}
		allowedNS = []string{"*"}
	} else if targetNamespace != "" && targetNamespace != "*" {
		if !strings.HasPrefix(targetNamespace, "idlistack-") {
			targetNamespace = "idlistack-" + strings.ToLower(targetNamespace)
		}
		nsGroup := "ns:" + targetNamespace
		ensureKeycloakGroupExists(ctx, client, kcURL, adminTok.AccessToken, nsGroup)
		groups = append(groups, nsGroup)
		allowedNS = []string{targetNamespace}
	}

	for _, g := range groups {
		ensureKeycloakGroupExists(ctx, client, kcURL, adminTok.AccessToken, g)
	}

	// 4. Create User in Keycloak `idlistack` realm
	userPayload := map[string]any{
		"username":        username,
		"email":           email,
		"firstName":       username,
		"lastName":        "User",
		"enabled":         true,
		"emailVerified":   true,
		"requiredActions": []string{},
		"credentials": []map[string]any{
			{
				"type":      "password",
				"value":     password,
				"temporary": false,
			},
		},
	}
	payloadBytes, _ := json.Marshal(userPayload)

	usersURL := fmt.Sprintf("%s/admin/realms/%s/users", kcURL, k8s.KeycloakRealm)
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, usersURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return err
	}
	createReq.Header.Set("Authorization", "Bearer "+adminTok.AccessToken)
	createReq.Header.Set("Content-Type", "application/json")

	createResp, err := client.Do(createReq)
	if err != nil {
		return fmt.Errorf("failed to create user in Keycloak: %w", err)
	}
	defer createResp.Body.Close()

	if createResp.StatusCode != http.StatusCreated && createResp.StatusCode != http.StatusConflict && createResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(createResp.Body)
		return fmt.Errorf("Keycloak user creation failed (HTTP %d): %s", createResp.StatusCode, string(body))
	}

	// 5. Look up user ID to ensure profile, password, and dynamic group memberships are assigned
	if userID := findKeycloakUserID(ctx, client, kcURL, adminTok.AccessToken, username); userID != "" {
		updateReq, _ := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/admin/realms/%s/users/%s", kcURL, k8s.KeycloakRealm, userID), bytes.NewBuffer(payloadBytes))
		if updateReq != nil {
			updateReq.Header.Set("Authorization", "Bearer "+adminTok.AccessToken)
			updateReq.Header.Set("Content-Type", "application/json")
			if uResp, err := client.Do(updateReq); err == nil {
				uResp.Body.Close()
			}
		}
		resetKeycloakUserPassword(ctx, client, kcURL, adminTok.AccessToken, userID, password)
		for _, g := range groups {
			if groupID := findKeycloakGroupID(ctx, client, kcURL, adminTok.AccessToken, g); groupID != "" {
				assignUserToKeycloakGroup(ctx, client, kcURL, adminTok.AccessToken, userID, groupID)
			}
		}
	}

	// 6. Dynamically provision K3s RBAC bindings for the new user immediately
	_, _ = k8s.EnsureNamespaceRBAC(ctx, username, groups, allowedNS, targetNamespace)
	return nil
}

// RegisterAndLoginKeycloakUser dynamically creates the user in Keycloak + K3s RBAC and immediately logs them in
func RegisterAndLoginKeycloakUser(ctx context.Context, username, email, password, role, targetNamespace string) (*Credentials, error) {
	if err := CreateKeycloakUser(ctx, username, email, password, role, targetNamespace); err != nil {
		return nil, err
	}
	var allowedNS []string
	if strings.EqualFold(role, "admin") || strings.EqualFold(role, "cluster-wide") {
		allowedNS = []string{"*"}
	} else if targetNamespace != "" {
		if !strings.HasPrefix(targetNamespace, "idlistack-") {
			targetNamespace = "idlistack-" + strings.ToLower(targetNamespace)
		}
		allowedNS = []string{targetNamespace}
	}
	kcURL := k8s.GetKeycloakBaseURL(ctx)
	return LoginWithKeycloak(ctx, kcURL, k8s.KeycloakRealm, k8s.KeycloakClientID, username, password, allowedNS)
}

func ensureKeycloakGroupExists(ctx context.Context, client *http.Client, kcURL, adminToken, groupName string) {
	groupsURL := fmt.Sprintf("%s/admin/realms/%s/groups", kcURL, k8s.KeycloakRealm)
	body, _ := json.Marshal(map[string]any{"name": groupName})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, groupsURL, bytes.NewBuffer(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func findKeycloakUserID(ctx context.Context, client *http.Client, kcURL, adminToken, username string) string {
	uURL := fmt.Sprintf("%s/admin/realms/%s/users?username=%s&exact=true", kcURL, k8s.KeycloakRealm, url.QueryEscape(username))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var users []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&users); err == nil && len(users) > 0 {
		if id, ok := users[0]["id"].(string); ok {
			return id
		}
	}
	return ""
}

func resetKeycloakUserPassword(ctx context.Context, client *http.Client, kcURL, adminToken, userID, password string) {
	pwURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/reset-password", kcURL, k8s.KeycloakRealm, userID)
	body, _ := json.Marshal(map[string]any{
		"type":      "password",
		"value":     password,
		"temporary": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, pwURL, bytes.NewBuffer(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func findKeycloakGroupID(ctx context.Context, client *http.Client, kcURL, adminToken, groupName string) string {
	gURL := fmt.Sprintf("%s/admin/realms/%s/groups?search=%s", kcURL, k8s.KeycloakRealm, url.QueryEscape(groupName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var groups []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&groups); err == nil {
		for _, g := range groups {
			if name, _ := g["name"].(string); strings.EqualFold(name, groupName) {
				if id, ok := g["id"].(string); ok {
					return id
				}
			}
		}
	}
	return ""
}

func assignUserToKeycloakGroup(ctx context.Context, client *http.Client, kcURL, adminToken, userID, groupID string) {
	joinURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/groups/%s", kcURL, k8s.KeycloakRealm, userID, groupID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, joinURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func enableRealmSelfRegistration(ctx context.Context, client *http.Client, kcURL, adminToken string) {
	realmURL := fmt.Sprintf("%s/admin/realms/%s", kcURL, k8s.KeycloakRealm)
	body, _ := json.Marshal(map[string]any{
		"realm":               k8s.KeycloakRealm,
		"enabled":             true,
		"registrationAllowed": true,
		"defaultGroups":       []string{"/developers"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, realmURL, bytes.NewBuffer(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

