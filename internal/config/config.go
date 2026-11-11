package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/idlistack/cli/internal/buildplan"
)

const (
	// ProjectStateFileName is the single JSON file representing the project state
	ProjectStateFileName = ".idlistack.json"
)

// Config represents the idlistack.toml configuration
type Config struct {
	Project ProjectConfig     `toml:"project"`
	Build   BuildConfig       `toml:"build"`
	Deploy  DeployConfig      `toml:"deploy"`
	Env     map[string]string `toml:"env"`
}

type ProjectConfig struct {
	Name string `toml:"name"`
}

type BuildConfig struct {
	Provider      string `toml:"provider"`
	Runtime       string `toml:"runtime"`
	PreInstallCmd string `toml:"pre_install_cmd"`
	InstallCmd    string `toml:"install_cmd"`
	BuildCmd      string `toml:"build_cmd"`
	StartCmd      string `toml:"start_cmd"`
}

type DeployConfig struct {
	Namespace       string   `toml:"namespace" json:"namespace,omitempty"`
	Port            int      `toml:"port" json:"port,omitempty"`
	Replicas        int      `toml:"replicas" json:"replicas,omitempty"`
	HealthCheckPath string   `toml:"health_check_path" json:"healthCheckPath,omitempty"`
	Dependencies    []string `toml:"dependencies" json:"dependencies,omitempty"`
	Registry        string   `toml:"registry" json:"registry,omitempty"`
	Image           string   `toml:"image" json:"image,omitempty"`
}

// ProjectState represents the single unified .idlistack.json file in the project directory
type ProjectState struct {
	Version     string            `json:"version"`
	ProjectName string            `json:"projectName,omitempty"` // legacy alias
	ProjectDir  string            `json:"projectDir,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`   // legacy alias
	Project     ProjectConfig     `json:"project"`
	Build       BuildConfig       `json:"build"`
	Deploy      DeployConfig      `json:"deploy"`
	Env         map[string]string `json:"env,omitempty"`
	Plan        *buildplan.Plan   `json:"plan,omitempty"`
	LastDeploy  string            `json:"lastDeploy,omitempty"`
	LastImage   string            `json:"lastImage,omitempty"`
	LastStatus  string            `json:"lastStatus,omitempty"`
	AccessURL   string            `json:"accessUrl,omitempty"`
	DataSynced  bool              `json:"dataSynced,omitempty"`
}

// AbsorbConfig absorbs settings from a Config struct into ProjectState
func (s *ProjectState) AbsorbConfig(cfg *Config) {
	if cfg == nil {
		return
	}
	if cfg.Project.Name != "" {
		s.Project.Name = cfg.Project.Name
		s.ProjectName = cfg.Project.Name
	}
	if cfg.Build.Provider != "" {
		s.Build.Provider = cfg.Build.Provider
	}
	if cfg.Build.Runtime != "" {
		s.Build.Runtime = cfg.Build.Runtime
	}
	if cfg.Build.PreInstallCmd != "" {
		s.Build.PreInstallCmd = cfg.Build.PreInstallCmd
	}
	if cfg.Build.InstallCmd != "" {
		s.Build.InstallCmd = cfg.Build.InstallCmd
	}
	if cfg.Build.BuildCmd != "" {
		s.Build.BuildCmd = cfg.Build.BuildCmd
	}
	if cfg.Build.StartCmd != "" {
		s.Build.StartCmd = cfg.Build.StartCmd
	}
	if cfg.Deploy.Namespace != "" {
		s.Deploy.Namespace = cfg.Deploy.Namespace
		s.Namespace = cfg.Deploy.Namespace
	}
	if cfg.Deploy.Port > 0 {
		s.Deploy.Port = cfg.Deploy.Port
	}
	if cfg.Deploy.Replicas > 0 {
		s.Deploy.Replicas = cfg.Deploy.Replicas
	}
	if cfg.Deploy.HealthCheckPath != "" {
		s.Deploy.HealthCheckPath = cfg.Deploy.HealthCheckPath
	}
	if len(cfg.Deploy.Dependencies) > 0 {
		s.Deploy.Dependencies = cfg.Deploy.Dependencies
	}
	if len(cfg.Env) > 0 {
		if s.Env == nil {
			s.Env = make(map[string]string)
		}
		for k, v := range cfg.Env {
			s.Env[k] = v
		}
	}
}

// ToConfig converts the ProjectState into a runtime *Config
func (s *ProjectState) ToConfig() *Config {
	name := s.Project.Name
	if name == "" {
		name = s.ProjectName
	}
	ns := s.Deploy.Namespace
	if ns == "" {
		ns = s.Namespace
	}
	if ns == "" && name != "" {
		ns = name
	}
	cfg := &Config{
		Project: ProjectConfig{Name: name},
		Build:   s.Build,
		Deploy:  s.Deploy,
		Env:     s.Env,
	}
	cfg.Deploy.Namespace = ns
	if cfg.Deploy.Replicas == 0 {
		cfg.Deploy.Replicas = 1
	}
	if cfg.Deploy.HealthCheckPath == "" {
		cfg.Deploy.HealthCheckPath = "/health"
	}
	return cfg
}

// ProjectStatePath returns the path to .idlistack.json in projectDir
func ProjectStatePath(projectDir string) string {
	return filepath.Join(projectDir, ProjectStateFileName)
}

// LoadProjectState loads .idlistack.json from projectDir, with transparent migration from legacy .idlistack/ directory
func LoadProjectState(projectDir string) (*ProjectState, error) {
	statePath := ProjectStatePath(projectDir)
	legacyDir := filepath.Join(projectDir, ".idlistack")
	if data, err := os.ReadFile(statePath); err == nil {
		var state ProjectState
		if err := json.Unmarshal(data, &state); err == nil {
			// Proactively absorb legacy idlistack.toml if present
			tomlPath := filepath.Join(projectDir, "idlistack.toml")
			if _, statErr := os.Stat(tomlPath); statErr == nil {
				var tomlCfg Config
				if _, decErr := toml.DecodeFile(tomlPath, &tomlCfg); decErr == nil {
					state.AbsorbConfig(&tomlCfg)
					_ = SaveProjectState(projectDir, &state)
				}
				_ = os.Remove(tomlPath)
			}
			// Always clean up legacy directory if it still exists
			_ = os.RemoveAll(legacyDir)
			return &state, nil
		}
	}

	// Backward compatibility: check if legacy .idlistack/ directory exists
	if info, err := os.Stat(legacyDir); err == nil && info.IsDir() {
		state := &ProjectState{
			Version:    "1.0",
			ProjectDir: projectDir,
		}
		// Read legacy link.json
		if linkBytes, err := os.ReadFile(filepath.Join(legacyDir, "link.json")); err == nil {
			var link LinkConfig
			if json.Unmarshal(linkBytes, &link) == nil {
				state.ProjectName = link.ProjectName
				state.Project.Name = link.ProjectName
				state.LastDeploy = link.LastDeploy
				state.LastImage = link.LastImage
			}
		}
		// Read legacy buildplan.json
		if planBytes, err := os.ReadFile(filepath.Join(legacyDir, "buildplan.json")); err == nil {
			var plan buildplan.Plan
			if json.Unmarshal(planBytes, &plan) == nil {
				state.Plan = &plan
			}
		}
		// Check data_synced.lock
		if _, err := os.Stat(filepath.Join(legacyDir, "data_synced.lock")); err == nil {
			state.DataSynced = true
		}

		// Also check idlistack.toml if present
		tomlPath := filepath.Join(projectDir, "idlistack.toml")
		if _, statErr := os.Stat(tomlPath); statErr == nil {
			var tomlCfg Config
			if _, decErr := toml.DecodeFile(tomlPath, &tomlCfg); decErr == nil {
				state.AbsorbConfig(&tomlCfg)
			}
			_ = os.Remove(tomlPath)
		}

		if state.Project.Name != "" || state.ProjectName != "" || state.Plan != nil {
			_ = SaveProjectState(projectDir, state)
			_ = os.RemoveAll(legacyDir)
			return state, nil
		}
	}

	return nil, fmt.Errorf("%s not found in %s", ProjectStateFileName, projectDir)
}

// SaveProjectState atomically saves the ProjectState to .idlistack.json and removes legacy files
func SaveProjectState(projectDir string, state *ProjectState) error {
	if state == nil {
		return nil
	}
	if state.Version == "" {
		state.Version = "1.0"
	}
	if state.Project.Name == "" && state.ProjectName != "" {
		state.Project.Name = state.ProjectName
	}
	if state.Deploy.Namespace == "" && state.Namespace != "" {
		state.Deploy.Namespace = state.Namespace
	}

	statePath := ProjectStatePath(projectDir)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", ProjectStateFileName, err)
	}

	// Atomic write via temp file
	tmpFile := fmt.Sprintf("%s.tmp.%d", statePath, os.Getpid())
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", ProjectStateFileName, err)
	}
	if err := os.Rename(tmpFile, statePath); err != nil {
		_ = os.Remove(tmpFile)
		_ = os.WriteFile(statePath, data, 0644)
	}
	// Always purge legacy directory and legacy toml file
	_ = os.RemoveAll(filepath.Join(projectDir, ".idlistack"))
	_ = os.Remove(filepath.Join(projectDir, "idlistack.toml"))
	return nil
}

// LinkConfig represents the legacy .idlistack/link.json state for backward compatibility
type LinkConfig struct {
	ProjectName string `json:"projectName"`
	ProjectDir  string `json:"projectDir"`
	LastDeploy  string `json:"lastDeploy,omitempty"`
	LastImage   string `json:"lastImage,omitempty"`
}

// Load reads and parses configuration from .idlistack.json (and absorbs idlistack.toml if present)
func Load(dir string) (*Config, error) {
	// 1. Try reading .idlistack.json
	state, err := LoadProjectState(dir)
	if err == nil && state != nil && (state.Project.Name != "" || state.ProjectName != "") {
		return state.ToConfig(), nil
	}

	// 2. Fallback: check legacy idlistack.toml
	configPath := filepath.Join(dir, "idlistack.toml")
	if _, statErr := os.Stat(configPath); statErr == nil {
		var cfg Config
		if _, decErr := toml.DecodeFile(configPath, &cfg); decErr == nil {
			migratedState := &ProjectState{
				Version:     "1.0",
				ProjectName: cfg.Project.Name,
				Namespace:   cfg.Deploy.Namespace,
				Project:     cfg.Project,
				Build:       cfg.Build,
				Deploy:      cfg.Deploy,
				Env:         cfg.Env,
			}
			_ = SaveProjectState(dir, migratedState)
			_ = os.Remove(configPath)
			return migratedState.ToConfig(), nil
		}
	}

	return nil, fmt.Errorf("no %s configuration found in %s", ProjectStateFileName, dir)
}
