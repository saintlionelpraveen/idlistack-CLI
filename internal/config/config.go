package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
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
	BuildCmd      string `toml:"build_cmd"`
	StartCmd      string `toml:"start_cmd"`
}

type DeployConfig struct {
	Port            int      `toml:"port"`
	Replicas        int      `toml:"replicas"`
	HealthCheckPath string   `toml:"health_check_path"`
	Dependencies    []string `toml:"dependencies"`
}

// LinkConfig represents the internal .idlistack/link.json state
type LinkConfig struct {
	ProjectName string `json:"projectName"`
	ProjectDir  string `json:"projectDir"`
	LastDeploy  string `json:"lastDeploy,omitempty"`
	LastImage   string `json:"lastImage,omitempty"`
}

// Load reads and parses idlistack.toml from the given directory
func Load(dir string) (*Config, error) {
	configPath := filepath.Join(dir, "idlistack.toml")

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("idlistack.toml not found in %s", dir)
	}

	var cfg Config
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse idlistack.toml: %w", err)
	}

	// Defaults
	if cfg.Deploy.Replicas == 0 {
		cfg.Deploy.Replicas = 1
	}
	if cfg.Deploy.HealthCheckPath == "" {
		cfg.Deploy.HealthCheckPath = "/health"
	}

	return &cfg, nil
}
