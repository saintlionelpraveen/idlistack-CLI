package detect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/idlistack/cli/internal/config"
)

func TestDetectWithProviders_GhostCMS(t *testing.T) {
	// Setup a fake Ghost project
	dir := createTestProject(t, map[string]string{
		"package.json":            `{"name": "ghost"}`,
		".ghost-cli":              `{"name": "ghost-local"}`,
		"current":                 "5.96.0",
		"config.development.json": `{"server": {"port": 2368}}`,
	})
	os.MkdirAll(filepath.Join(dir, "versions"), 0755)

	plan, err := Detect(context.Background(), dir, nil, false)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if plan.Provider != "node" {
		t.Errorf("Expected provider 'node', got %s", plan.Provider)
	}

	if plan.DetectedFramework != "ghost" {
		t.Errorf("Expected framework 'ghost', got %s", plan.DetectedFramework)
	}

	if plan.Port != 2368 {
		t.Errorf("Expected port 2368, got %d", plan.Port)
	}
}

func TestDetect_EndToEnd_NoDockerfile(t *testing.T) {
	// Setup a simple Node app (no dockerfile)
	dir := createTestProject(t, map[string]string{
		"package.json": `{"name": "test", "scripts": {"start": "node server.js"}}`,
		"server.js":    "console.log('started');",
	})

	ctx := context.Background()
	plan, err := Detect(ctx, dir, nil, false)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	// Should be detected by Layer 1 (railpack or provider fallback)
	if plan.DetectionSource != "railpack" && plan.DetectionSource != "provider-node" {
		t.Errorf("Expected DetectionSource 'railpack' or 'provider-node', got %s", plan.DetectionSource)
	}
	if plan.Provider != "node" {
		t.Errorf("Expected Provider 'node', got %s", plan.Provider)
	}
	if plan.StartCmd != "npm run start" && plan.StartCmd != "node server.js" && plan.StartCmd != "npm start" {
		t.Errorf("Expected valid StartCmd, got %s", plan.StartCmd)
	}
}

func TestDetect_FinanceSystem_PythonRawScript(t *testing.T) {
	// Setup a script-only project without requirements.txt or Dockerfile (like raw finance-system)
	dir := createTestProject(t, map[string]string{
		"finance.py": "print('Finance system running')",
	})

	ctx := context.Background()
	plan, err := Detect(ctx, dir, nil, false)
	if err != nil {
		t.Fatalf("Detect failed for raw python script: %v", err)
	}

	if plan.Provider != "python" {
		t.Errorf("Expected Provider 'python', got %s", plan.Provider)
	}
	if plan.StartCmd != "python finance.py" {
		t.Errorf("Expected StartCmd 'python finance.py', got %s", plan.StartCmd)
	}
	if plan.Port != 8000 {
		t.Errorf("Expected default Port 8000, got %d", plan.Port)
	}
}

func TestDetect_FinanceSystem_SubfolderPython(t *testing.T) {
	// Setup a project with backend in subfolder
	dir := createTestProject(t, map[string]string{
		"backend/requirements.txt": "fastapi\nuvicorn",
		"backend/main.py":          "print('FastAPI started')",
	})

	ctx := context.Background()
	plan, err := Detect(ctx, dir, nil, false)
	if err != nil {
		t.Fatalf("Detect failed for subfolder python: %v", err)
	}

	if plan.Provider != "python" {
		t.Errorf("Expected Provider 'python', got %s", plan.Provider)
	}
	if plan.StartCmd != "python backend/main.py" && plan.StartCmd != "python main.py" {
		t.Errorf("Expected valid StartCmd, got %s", plan.StartCmd)
	}
}

func TestDetect_FinanceSystem_SubfolderNode(t *testing.T) {
	// Setup a project with server in subfolder
	dir := createTestProject(t, map[string]string{
		"server/package.json": `{"name": "finance-api", "scripts": {"start": "node index.js"}}`,
		"server/index.js":     "console.log('Finance server running')",
	})

	ctx := context.Background()
	plan, err := Detect(ctx, dir, nil, false)
	if err != nil {
		t.Fatalf("Detect failed for subfolder node: %v", err)
	}

	if plan.Provider != "node" {
		t.Errorf("Expected Provider 'node', got %s", plan.Provider)
	}
	if plan.StartCmd != "cd server && npm start" && plan.StartCmd != "npm start" {
		t.Errorf("Expected valid StartCmd, got %s", plan.StartCmd)
	}
}

func TestDetect_EmptyDir_NoNixpacksOrAIFallback(t *testing.T) {
	dir := t.TempDir()

	// Create a fake nixpacks binary on PATH that records if it was invoked
	fakeBinDir := t.TempDir()
	nixpacksMarker := filepath.Join(fakeBinDir, "nixpacks_invoked")
	fakeNixpacks := filepath.Join(fakeBinDir, "nixpacks")
	script := "#!/bin/sh\ntouch " + nixpacksMarker + "\necho '{\"variables\":{\"NIXPACKS_METADATA\":\"node\"},\"start\":{\"cmd\":\"node index.js\"}}'\n"
	if err := os.WriteFile(fakeNixpacks, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake nixpacks binary: %v", err)
	}

	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GEMINI_API_KEY", "test-fake-gemini-key")

	plan, err := Detect(context.Background(), dir, nil, false)
	if err == nil {
		t.Fatalf("Expected error for empty directory without nixpacks/AI fallback, got plan: %+v", plan)
	}
	if plan != nil {
		t.Errorf("Expected nil plan for empty directory, got: %+v", plan)
	}
	if _, statErr := os.Stat(nixpacksMarker); !os.IsNotExist(statErr) {
		t.Errorf("Expected nixpacks binary never to be invoked, but marker file exists")
	}
}

func TestDetect_ConfigProviderOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Build: config.BuildConfig{
			Provider: "node",
			Runtime:  "20",
			StartCmd: "node dist/main.js",
		},
		Deploy: config.DeployConfig{
			Port: 4000,
		},
	}

	plan, err := Detect(context.Background(), dir, cfg, false)
	if err != nil {
		t.Fatalf("Detect with config provider override failed: %v", err)
	}
	if plan.Provider != "node" {
		t.Errorf("Expected Provider 'node', got %s", plan.Provider)
	}
	if plan.DetectionSource != "config" {
		t.Errorf("Expected DetectionSource 'config', got %s", plan.DetectionSource)
	}
	if plan.Runtime != "20" {
		t.Errorf("Expected Runtime '20', got %s", plan.Runtime)
	}
	if plan.StartCmd != "node dist/main.js" {
		t.Errorf("Expected StartCmd 'node dist/main.js', got %s", plan.StartCmd)
	}
	if plan.Port != 4000 {
		t.Errorf("Expected Port 4000, got %d", plan.Port)
	}
}

func TestConfig_LegacyAISectionIgnored(t *testing.T) {
	dir := createTestProject(t, map[string]string{
		"idlistack.toml": `[project]
name = "legacy-app"

[build]
provider = "python"

[ai]
api_key = "legacy-gemini-key"
`,
	})

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Expected config.Load to succeed with legacy [ai] section, got error: %v", err)
	}
	if cfg.Project.Name != "legacy-app" {
		t.Errorf("Expected project name 'legacy-app', got %s", cfg.Project.Name)
	}
	if cfg.Build.Provider != "python" {
		t.Errorf("Expected build provider 'python', got %s", cfg.Build.Provider)
	}
}
