package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/idlistack/cli/internal/buildplan"
)

func TestProjectState_SaveAndLoad(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "idlistack-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	state := &ProjectState{
		Version:     "1.0",
		ProjectName: "test-app",
		Namespace:   "idlistack-test-app",
		Plan: &buildplan.Plan{
			Provider: "node",
			Runtime:  "20.0.0",
			Port:     3000,
		},
		LastDeploy: "2026-10-15T11:00:00Z",
		LastImage:  "idlistack/test-app:12345",
		LastStatus: "deployed",
		DataSynced: true,
	}

	if err := SaveProjectState(tmpDir, state); err != nil {
		t.Fatalf("SaveProjectState failed: %v", err)
	}

	// Verify .idlistack.json was created
	expectedPath := filepath.Join(tmpDir, ProjectStateFileName)
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("expected %s to exist: %v", expectedPath, err)
	}

	// Load back
	loaded, err := LoadProjectState(tmpDir)
	if err != nil {
		t.Fatalf("LoadProjectState failed: %v", err)
	}

	if loaded.ProjectName != "test-app" {
		t.Errorf("expected ProjectName 'test-app', got '%s'", loaded.ProjectName)
	}
	if loaded.Plan == nil || loaded.Plan.Provider != "node" {
		t.Errorf("expected Plan with provider 'node'")
	}
	if !loaded.DataSynced {
		t.Errorf("expected DataSynced to be true")
	}
}

func TestProjectState_LegacyMigration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "idlistack-legacy-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	legacyDir := filepath.Join(tmpDir, ".idlistack")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Legacy link.json
	link := LinkConfig{
		ProjectName: "legacy-app",
		LastDeploy:  "2026-10-01T12:00:00Z",
		LastImage:   "legacy:v1",
	}
	linkBytes, _ := json.Marshal(link)
	os.WriteFile(filepath.Join(legacyDir, "link.json"), linkBytes, 0644)

	// Legacy buildplan.json
	plan := buildplan.Plan{
		Provider: "python",
		Runtime:  "3.11",
		Port:     8000,
	}
	planBytes, _ := json.Marshal(plan)
	os.WriteFile(filepath.Join(legacyDir, "buildplan.json"), planBytes, 0644)

	// Legacy data_synced.lock
	os.WriteFile(filepath.Join(legacyDir, "data_synced.lock"), []byte("done"), 0644)

	// Load should transparently migrate
	state, err := LoadProjectState(tmpDir)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	if state.ProjectName != "legacy-app" {
		t.Errorf("expected migrated ProjectName 'legacy-app', got '%s'", state.ProjectName)
	}
	if state.Plan == nil || state.Plan.Provider != "python" {
		t.Errorf("expected migrated Plan with provider 'python'")
	}
	if !state.DataSynced {
		t.Errorf("expected migrated DataSynced to be true")
	}

	// Verify legacy directory is removed
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Errorf("expected legacy directory to be removed after migration")
	}

	// Verify .idlistack.json exists
	if _, err := os.Stat(filepath.Join(tmpDir, ProjectStateFileName)); err != nil {
		t.Errorf("expected %s to exist after migration", ProjectStateFileName)
	}
}

func TestProjectState_TomlMigration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "idlistack-toml-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tomlContent := `[project]
name = "my-toml-app"

[build]
provider = "python"
runtime = "3.11"
start_cmd = "uvicorn main:app"

[deploy]
port = 8080
replicas = 2
health_check_path = "/healthz"

[env]
DATABASE_URL = "postgres://user:pass@db:5432/mydb"
`
	tomlPath := filepath.Join(tmpDir, "idlistack.toml")
	if err := os.WriteFile(tomlPath, []byte(tomlContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Loading config via Load(tmpDir) should absorb toml into .idlistack.json and remove toml
	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Project.Name != "my-toml-app" {
		t.Errorf("expected Project.Name 'my-toml-app', got '%s'", cfg.Project.Name)
	}
	if cfg.Deploy.Port != 8080 {
		t.Errorf("expected Port 8080, got %d", cfg.Deploy.Port)
	}
	if cfg.Deploy.Replicas != 2 {
		t.Errorf("expected Replicas 2, got %d", cfg.Deploy.Replicas)
	}
	if cfg.Env["DATABASE_URL"] != "postgres://user:pass@db:5432/mydb" {
		t.Errorf("expected env DATABASE_URL to match")
	}

	// Verify idlistack.toml is deleted
	if _, err := os.Stat(tomlPath); !os.IsNotExist(err) {
		t.Errorf("expected idlistack.toml to be deleted, but it still exists")
	}

	// Verify .idlistack.json exists and has absorbed the config
	state, err := LoadProjectState(tmpDir)
	if err != nil {
		t.Fatalf("LoadProjectState failed: %v", err)
	}
	if state.Project.Name != "my-toml-app" {
		t.Errorf("expected state.Project.Name 'my-toml-app', got '%s'", state.Project.Name)
	}
	if state.Deploy.Port != 8080 {
		t.Errorf("expected state.Deploy.Port 8080, got %d", state.Deploy.Port)
	}
}
