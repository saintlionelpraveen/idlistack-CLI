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
		raw := strings.Trim(g, "/")
		parts := strings.Split(raw, "/")
		leaf := strings.ToLower(parts[len(parts)-1])

		if leaf == "cluster-admins" || leaf == "admin" || leaf == "admins" {
			scope = "cluster-wide"
		}
		if strings.HasPrefix(leaf, "ns:") {
			ns := strings.TrimPrefix(leaf, "ns:")
			if !strings.HasPrefix(ns, "idlistack-") && ns != "*" {
				ns = "idlistack-" + ns
			}
			tokenNamespaces = appendUnique(tokenNamespaces, ns)
		} else {
			// Also inspect each segment in case ns: is in path
			for _, part := range parts {
				pLower := strings.ToLower(part)
				if strings.HasPrefix(pLower, "ns:") {
					ns := strings.TrimPrefix(pLower, "ns:")
					if !strings.HasPrefix(ns, "idlistack-") && ns != "*" {
						ns = "idlistack-" + ns
					}
					tokenNamespaces = appendUnique(tokenNamespaces, ns)
				}
			}
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

// RefreshSession refreshes an expired token using Keycloak refresh_token
func RefreshSession(ctx context.Context, creds *Credentials) (*Credentials, error) {
	if creds == nil {
		return nil, fmt.Errorf("session expired; please run 'idlistack login'")
	}
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
	if creds == nil || creds.AccessToken == "" {
		return fmt.Errorf("no active session")
	}

	if creds.KeycloakURL == "" {
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
		if rbacErr != nil && strings.Contains(rbacErr.Error(), "RBAC denied") {
			return nil, rbacErr
		}
		if creds.Scope != "cluster-wide" {
			creds.AllowedNamespaces = appendUnique(creds.AllowedNamespaces, targetNamespace)
		}
		if k8sToken != "" {
			creds.K8sToken = k8sToken
		}
		_ = SaveCredentials(creds)
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

// KeycloakParentGroup defines the container group in Keycloak
const KeycloakParentGroup = "CLI - Deploy"

func ensureKeycloakUserProfileAllowsMinLength(ctx context.Context, client *http.Client, kcURL, adminToken string, minLength int) {
	upURL := fmt.Sprintf("%s/admin/realms/%s/users/profile", kcURL, k8s.KeycloakRealm)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var profile map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return
	}
	attrs, ok := profile["attributes"].([]any)
	if !ok {
		return
	}
	modified := false
	for _, a := range attrs {
		attrMap, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := attrMap["name"].(string); name == "username" {
			val, _ := attrMap["validations"].(map[string]any)
			if val != nil {
				lenVal, _ := val["length"].(map[string]any)
				if lenVal != nil {
					if currentMin, ok := lenVal["min"].(float64); ok && currentMin > float64(minLength) {
						lenVal["min"] = minLength
						modified = true
					}
				}
			}
		}
		if name, _ := attrMap["name"].(string); name == "lastName" || name == "firstName" {
			if _, hasReq := attrMap["required"]; hasReq {
				delete(attrMap, "required")
				modified = true
			}
		}
	}
	if modified {
		data, _ := json.Marshal(profile)
		putReq, _ := http.NewRequestWithContext(ctx, http.MethodPut, upURL, bytes.NewBuffer(data))
		if putReq != nil {
			putReq.Header.Set("Authorization", "Bearer "+adminToken)
			putReq.Header.Set("Content-Type", "application/json")
			if pResp, err := client.Do(putReq); err == nil {
				pResp.Body.Close()
			}
		}
	}
}

// CreateKeycloakUser dynamically creates a new user in Keycloak (`idlistack` realm),
// assigns them under the `CLI - Deploy` group and their role/namespace groups,
// and provisions their K3s RBAC bindings automatically.
func CreateKeycloakUser(ctx context.Context, username, email, firstName, lastName, password, role, targetNamespace string) error {
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	firstName = strings.TrimSpace(firstName)
	lastName = strings.TrimSpace(lastName)
	if username == "" || password == "" {
		return fmt.Errorf("username and password are required")
	}
	if email == "" {
		email = fmt.Sprintf("%s@%s.local", strings.ToLower(username), k8s.KeycloakRealm)
	}
	if firstName == "" {
		firstName = username
	}
	if lastName == "" {
		lastName = "User"
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

	// 2. Ensure Keycloak user profile allows usernames of length >= 2
	ensureKeycloakUserProfileAllowsMinLength(ctx, client, kcURL, adminTok.AccessToken, 2)

	// 3. Ensure group 'CLI - Deploy' exists in Keycloak
	ensureKeycloakGroupExists(ctx, client, kcURL, adminTok.AccessToken, KeycloakParentGroup)
	parentGroupID := findKeycloakGroupID(ctx, client, kcURL, adminTok.AccessToken, KeycloakParentGroup)

	// 4. Determine dynamic groups
	isClusterAdmin := strings.EqualFold(role, "admin") || strings.EqualFold(role, "cluster-wide") || strings.EqualFold(role, "cluster-admin")
	roleGroup := "developers"
	var allowedNS []string

	if isClusterAdmin {
		roleGroup = "cluster-admins"
		allowedNS = []string{"*"}
	} else if targetNamespace != "" && targetNamespace != "*" {
		if !strings.HasPrefix(targetNamespace, "idlistack-") {
			targetNamespace = "idlistack-" + strings.ToLower(targetNamespace)
		}
		allowedNS = []string{targetNamespace}
	}

	// Ensure role group ('developers' or 'cluster-admins') exists in Keycloak
	ensureKeycloakGroupExists(ctx, client, kcURL, adminTok.AccessToken, roleGroup)
	roleGroupID := findKeycloakGroupID(ctx, client, kcURL, adminTok.AccessToken, roleGroup)

	// Ensure namespace group exists if scoped
	if len(allowedNS) > 0 && allowedNS[0] != "*" {
		ensureKeycloakGroupExists(ctx, client, kcURL, adminTok.AccessToken, "ns:"+allowedNS[0])
	}

	// 5. Ensure realm self-registration is enabled on idlistack realm with defaultGroups
	enableRealmSelfRegistration(ctx, client, kcURL, adminTok.AccessToken)

	// Dynamic groups list for RBAC evaluation
	rbacGroups := []string{KeycloakParentGroup, username, roleGroup}
	if len(allowedNS) > 0 && allowedNS[0] != "*" {
		rbacGroups = append(rbacGroups, "ns:"+allowedNS[0])
	}

	// 6. Create User in Keycloak `idlistack` realm
	userPayload := map[string]any{
		"username":        username,
		"email":           email,
		"firstName":       firstName,
		"lastName":        lastName,
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

	if createResp.StatusCode == http.StatusConflict {
		return fmt.Errorf("user '%s' or email already exists in Keycloak. Please sign in.", username)
	}

	if createResp.StatusCode != http.StatusCreated && createResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(createResp.Body)
		return fmt.Errorf("Keycloak user creation failed (HTTP %d): %s", createResp.StatusCode, string(body))
	}

	// 7. Look up user ID to ensure profile, password, and group memberships are assigned
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

		// Assign to group 'CLI - Deploy'
		if parentGroupID != "" {
			assignUserToKeycloakGroup(ctx, client, kcURL, adminTok.AccessToken, userID, parentGroupID)
		}

		// Assign to role group ('developers' or 'cluster-admins')
		if roleGroupID != "" {
			assignUserToKeycloakGroup(ctx, client, kcURL, adminTok.AccessToken, userID, roleGroupID)
		}

		// Assign to namespace group if scoped
		if len(allowedNS) > 0 && allowedNS[0] != "*" {
			if topNSID := findKeycloakGroupID(ctx, client, kcURL, adminTok.AccessToken, "ns:"+allowedNS[0]); topNSID != "" {
				assignUserToKeycloakGroup(ctx, client, kcURL, adminTok.AccessToken, userID, topNSID)
			}
		}
	}

	// 8. Provision K3s RBAC bindings for the new user immediately
	_, _ = k8s.EnsureNamespaceRBAC(ctx, username, rbacGroups, allowedNS, targetNamespace)
	return nil
}

// RegisterAndLoginKeycloakUser dynamically creates the user in Keycloak + K3s RBAC and immediately logs them in
func RegisterAndLoginKeycloakUser(ctx context.Context, username, email, firstName, lastName, password, role, targetNamespace string) (*Credentials, error) {
	if err := CreateKeycloakUser(ctx, username, email, firstName, lastName, password, role, targetNamespace); err != nil {
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
	if findKeycloakGroupID(ctx, client, kcURL, adminToken, groupName) != "" {
		return
	}
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

func ensureKeycloakChildGroupExists(ctx context.Context, client *http.Client, kcURL, adminToken, parentGroupID, childGroupName string) {
	if parentGroupID == "" || childGroupName == "" {
		return
	}
	if id := findKeycloakChildGroupID(ctx, client, kcURL, adminToken, parentGroupID, childGroupName); id != "" {
		return
	}
	cURL := fmt.Sprintf("%s/admin/realms/%s/groups/%s/children", kcURL, k8s.KeycloakRealm, parentGroupID)
	body, _ := json.Marshal(map[string]any{"name": childGroupName})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cURL, bytes.NewBuffer(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func findKeycloakChildGroupID(ctx context.Context, client *http.Client, kcURL, adminToken, parentGroupID, childGroupName string) string {
	if parentGroupID == "" || childGroupName == "" {
		return ""
	}
	cURL := fmt.Sprintf("%s/admin/realms/%s/groups/%s/children", kcURL, k8s.KeycloakRealm, parentGroupID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var children []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&children); err == nil {
		for _, c := range children {
			if name, _ := c["name"].(string); strings.EqualFold(name, childGroupName) {
				if id, ok := c["id"].(string); ok {
					return id
				}
			}
		}
	}
	return ""
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
		"defaultGroups":       []string{"/" + KeycloakParentGroup, "/developers"},
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

// KeycloakUserInfo represents user information retrieved from Keycloak Admin API
type KeycloakUserInfo struct {
	ID        string   `json:"id"`
	Username  string   `json:"username"`
	Email     string   `json:"email"`
	FirstName string   `json:"firstName"`
	LastName  string   `json:"lastName"`
	Enabled   bool     `json:"enabled"`
	Groups    []string `json:"groups,omitempty"`
}

// ListKeycloakUsers queries the Keycloak Admin REST API for all registered users in the realm
func ListKeycloakUsers(ctx context.Context) ([]KeycloakUserInfo, error) {
	kcURL := k8s.GetKeycloakBaseURL(ctx)
	client := &http.Client{Timeout: 10 * time.Second}

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
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var adminTok oidcTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&adminTok); err != nil || adminTok.AccessToken == "" {
		return nil, fmt.Errorf("failed to authenticate with Keycloak Admin API")
	}

	uURL := fmt.Sprintf("%s/admin/realms/%s/users?max=200", kcURL, k8s.KeycloakRealm)
	uReq, err := http.NewRequestWithContext(ctx, http.MethodGet, uURL, nil)
	if err != nil {
		return nil, err
	}
	uReq.Header.Set("Authorization", "Bearer "+adminTok.AccessToken)
	uResp, err := client.Do(uReq)
	if err != nil {
		return nil, err
	}
	defer uResp.Body.Close()

	var rawUsers []struct {
		ID        string `json:"id"`
		Username  string `json:"username"`
		Email     string `json:"email"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Enabled   bool   `json:"enabled"`
	}
	if err := json.NewDecoder(uResp.Body).Decode(&rawUsers); err != nil {
		return nil, err
	}

	var users []KeycloakUserInfo
	for _, ru := range rawUsers {
		userInfo := KeycloakUserInfo{
			ID:        ru.ID,
			Username:  ru.Username,
			Email:     ru.Email,
			FirstName: ru.FirstName,
			LastName:  ru.LastName,
			Enabled:   ru.Enabled,
		}
		gURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/groups", kcURL, k8s.KeycloakRealm, ru.ID)
		gReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, gURL, nil)
		if gReq != nil {
			gReq.Header.Set("Authorization", "Bearer "+adminTok.AccessToken)
			if gResp, err := client.Do(gReq); err == nil {
				var grps []struct {
					Name string `json:"name"`
				}
				if json.NewDecoder(gResp.Body).Decode(&grps) == nil {
					for _, g := range grps {
						userInfo.Groups = append(userInfo.Groups, g.Name)
					}
				}
				gResp.Body.Close()
			}
		}
		users = append(users, userInfo)
	}
	return users, nil
}


