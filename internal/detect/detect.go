package detect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/buildplan"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/ui"
	"github.com/mattn/go-isatty"
	"gopkg.in/yaml.v3"
)

func applyConfigOverrides(plan *buildplan.Plan, cfg *config.Config) {
	if cfg == nil {
		return
	}
	if cfg.Build.Provider != "" {
		plan.Provider = cfg.Build.Provider
	}
	if cfg.Build.Runtime != "" {
		plan.Runtime = cfg.Build.Runtime
	}
	if cfg.Build.PreInstallCmd != "" {
		plan.PreInstallCmd = cfg.Build.PreInstallCmd
	}
	if cfg.Build.BuildCmd != "" {
		plan.BuildCmd = cfg.Build.BuildCmd
	}
	if cfg.Build.StartCmd != "" {
		plan.StartCmd = cfg.Build.StartCmd
	}
	if cfg.Deploy.Port != 0 {
		plan.Port = cfg.Deploy.Port
	}
}

// Detect runs the detection pipeline:
//
//	Layer 0: Check for existing Dockerfile/docker-compose.yml
//	Layer 0.5: Dynamic Rules Detection
//	Layer 1: Railpack Detection (primary engine)
//	Layer 2: Deep heuristic recursive fallback (safety net)
//
// When both a Dockerfile and a provider match exist, an interactive
// terminal prompt lets the user choose which build strategy to use.
func Detect(ctx context.Context, projectDir string, cfg *config.Config, verbose bool) (*buildplan.Plan, error) {
	// ─── Layer 0: Existing Dockerfile / Docker Compose ──────────────
	layer0Plan, err0 := detectDockerAndCompose(projectDir)

	// ─── Layer 0.5: Dynamic Rules Detection ─────────────────────────
	layer05Plan, err05 := detectWithRules(projectDir, cfg, verbose)

	// ─── Layer 1: Railpack Detection (Railway primary builder) ─────
	layerRailpackPlan, errRp := DetectWithRailpack(ctx, projectDir, cfg, verbose)

	// If Layer 0.5 found something and there's also a Dockerfile, prompt the user
	if layer0Plan != nil && layer05Plan != nil && (cfg == nil || cfg.Build.Provider == "") {
		if IsInteractiveTerminal() {
			fmt.Println()
			ui.Info(fmt.Sprintf("Existing container setup found: %s", color.CyanString(layer0Plan.DockerfilePath)))
			ui.Info(fmt.Sprintf("Detected framework signature (rules): %s (%s)", color.CyanString(layer05Plan.DetectedFramework), color.CyanString(layer05Plan.Provider)))
			fmt.Println()
			fmt.Println("  Choose build method:")
			fmt.Printf("    [1] Use existing Dockerfile (%s)\n", layer0Plan.DockerfilePath)
			fmt.Printf("    [2] Use IdliStack Zero-Config Buildpack (%s / %s)\n", layer05Plan.DetectedFramework, layer05Plan.Provider)
			fmt.Println()
			fmt.Print("  Select option [1/2] (default 1): ")

			var choice string
			fmt.Scanln(&choice)
			choice = strings.TrimSpace(choice)

			if choice == "2" {
				applyConfigOverrides(layer05Plan, cfg)
				return layer05Plan, nil
			}
		}
	}

	// ─── Return the best available plan ─────────────────────────────

	// Layer 0.5 with a custom Dockerfile template (e.g. Ghost, Frappe)
	// takes absolute priority — these are specialized images that cannot
	// be reproduced by generic provider plans.
	if layer05Plan != nil && err05 == nil && layer05Plan.DockerfilePath != "" {
		if verbose {
			ui.Detail("Detected by dynamic rules with custom Dockerfile: %s", color.CyanString(layer05Plan.DetectedFramework))
		}
		applyConfigOverrides(layer05Plan, cfg)
		layer05Plan.Normalize()
		return layer05Plan, nil
	}

	// Layer 0: Dockerfile wins by default when present
	if layer0Plan != nil && err0 == nil {
		applyConfigOverrides(layer0Plan, cfg)
		layer0Plan.Normalize()
		return layer0Plan, nil
	}

	// Configured Provider wins over everything else
	if cfg != nil && cfg.Build.Provider != "" {
		if layerRailpackPlan != nil && errRp == nil && layerRailpackPlan.DetectionSource == "railpack" && layerRailpackPlan.Provider == cfg.Build.Provider {
			applyConfigOverrides(layerRailpackPlan, cfg)
			layerRailpackPlan.Normalize()
			return layerRailpackPlan, nil
		}
		if layer05Plan != nil && err05 == nil && layer05Plan.Provider == cfg.Build.Provider {
			applyConfigOverrides(layer05Plan, cfg)
			layer05Plan.Normalize()
			return layer05Plan, nil
		}
		if layerRailpackPlan != nil && errRp == nil && layerRailpackPlan.Provider == cfg.Build.Provider {
			applyConfigOverrides(layerRailpackPlan, cfg)
			layerRailpackPlan.Normalize()
			return layerRailpackPlan, nil
		}
		// Fallback: build directly using the configured provider
		configPlan := buildplan.NewDefaultPlan()
		configPlan.Provider = cfg.Build.Provider
		configPlan.Stack = cfg.Build.Provider
		configPlan.DetectionSource = "config"
		configPlan.DetectionConfidence = "high"
		applyConfigOverrides(configPlan, cfg)
		configPlan.Normalize()
		return configPlan, nil
	}

	// Layer 0.5: Dynamic Rules Detection
	if layer05Plan != nil && err05 == nil {
		if verbose {
			ui.Detail("Detected by dynamic rules: %s", color.CyanString(layer05Plan.DetectedFramework))
		}
		applyConfigOverrides(layer05Plan, cfg)
		layer05Plan.Normalize()
		return layer05Plan, nil
	}

	// Layer 1: Railpack Detection (Primary) & Layer 2: Deep heuristic recursive fallback (Safety net)
	if layerRailpackPlan != nil && errRp == nil {
		if verbose {
			if layerRailpackPlan.DetectionSource == "heuristic" {
				ui.Detail("Detected by heuristic fallback: %s (%s)", layerRailpackPlan.Stack, layerRailpackPlan.StartCmd)
			} else {
				ui.Detail("Detected by Railpack: %s (%s)", color.CyanString(layerRailpackPlan.Stack), color.HiBlackString(layerRailpackPlan.Runtime))
			}
		}
		applyConfigOverrides(layerRailpackPlan, cfg)
		layerRailpackPlan.Normalize()
		return layerRailpackPlan, nil
	}

	return nil, fmt.Errorf("could not detect application type. Create a Dockerfile or ensure your project has a recognizable structure")
}

func IsInteractiveTerminal() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

// ─── Layer 0: Dockerfile & Docker-Compose Detection ─────────────────────

type ComposeConfig struct {
	Services map[string]ComposeService `yaml:"services"`
}

type ComposeService struct {
	Image       string      `yaml:"image"`
	Build       interface{} `yaml:"build"`
	Ports       []string    `yaml:"ports"`
	Environment interface{} `yaml:"environment"`
	Volumes     []string    `yaml:"volumes"`
	Command     interface{} `yaml:"command"`
}

func detectDockerAndCompose(projectDir string) (*buildplan.Plan, error) {
	plan := buildplan.NewDefaultPlan()
	var foundDockerfile string
	var detectedPort int
	var detectedCmd string
	var detectedImage string
	var detectedEnv map[string]string
	var detectedVolumes []string

	// 1. Check for docker-compose.yml / compose.yml
	composeFiles := []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}
	for _, cf := range composeFiles {
		cfPath := filepath.Join(projectDir, cf)
		if data, err := os.ReadFile(cfPath); err == nil {
			var cfg ComposeConfig
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				continue // If parsing fails, try next file
			}

			// Find primary service. Usually the one with ports, or just the first non-database service
			var primaryService *ComposeService
			for name, svc := range cfg.Services {
				// Very basic heuristic to skip databases if there are multiple services
				if len(cfg.Services) > 1 && (strings.Contains(name, "db") || strings.Contains(name, "redis") || strings.Contains(name, "postgres") || strings.Contains(name, "mysql")) {
					continue
				}
				primaryService = &svc
				break
			}

			if primaryService == nil {
				continue // No valid service found
			}

			detectedImage = primaryService.Image

			// Extract dockerfile path from build context if present
			if primaryService.Build != nil {
				switch b := primaryService.Build.(type) {
				case string:
					// e.g. build: .
					if _, err := os.Stat(filepath.Join(projectDir, "Dockerfile")); err == nil {
						foundDockerfile = "Dockerfile"
					}
				case map[string]interface{}:
					// e.g. build: { context: ., dockerfile: alt.Dockerfile }
					dfCandidate := "Dockerfile"
					if df, ok := b["dockerfile"].(string); ok {
						dfCandidate = df
					}
					if _, err := os.Stat(filepath.Join(projectDir, dfCandidate)); err == nil {
						foundDockerfile = dfCandidate
					}
				}
			}

			// Extract Ports
			for _, portStr := range primaryService.Ports {
				// Port string can be "3000:3000", "8080", etc.
				parts := strings.Split(portStr, ":")
				portToParse := parts[len(parts)-1] // Take container port
				var p int
				if _, err := fmt.Sscanf(portToParse, "%d", &p); err == nil {
					if p != 5432 && p != 6379 && p != 3306 && p != 27017 {
						detectedPort = p
						break
					} else if detectedPort == 0 {
						detectedPort = p
					}
				}
			}

			// Extract Environment
			if primaryService.Environment != nil {
				detectedEnv = make(map[string]string)
				switch e := primaryService.Environment.(type) {
				case map[string]interface{}:
					for k, v := range e {
						detectedEnv[k] = fmt.Sprintf("%v", v)
					}
				case []interface{}:
					for _, v := range e {
						str := fmt.Sprintf("%v", v)
						parts := strings.SplitN(str, "=", 2)
						if len(parts) == 2 {
							detectedEnv[parts[0]] = parts[1]
						} else if len(parts) == 1 {
							// If value is missing, some composes leave it empty or fetch from host
							detectedEnv[parts[0]] = ""
						}
					}
				}
			}

			// Extract Volumes
			if len(primaryService.Volumes) > 0 {
				detectedVolumes = primaryService.Volumes
			}

			// Extract Command
			if primaryService.Command != nil {
				switch c := primaryService.Command.(type) {
				case string:
					detectedCmd = c
				case []interface{}:
					var parts []string
					for _, v := range c {
						parts = append(parts, fmt.Sprintf("%v", v))
					}
					detectedCmd = strings.Join(parts, " ")
				}
			}

			break // Successfully parsed one compose file
		}
	}

	// 2. Search common Dockerfile paths if not explicitly set by docker-compose
	if foundDockerfile == "" {
		dockerfileCandidates := []string{
			"Dockerfile",
			"docker/Dockerfile",
			"deploy/Dockerfile",
			"build/Dockerfile",
			"Dockerfile.prod",
			"Dockerfile.dev",
		}
		for _, dfCandidate := range dockerfileCandidates {
			if _, err := os.Stat(filepath.Join(projectDir, dfCandidate)); err == nil {
				foundDockerfile = dfCandidate
				break
			}
		}
	}

	// 3a. Compose with a pre-built image (no Dockerfile needed)
	if detectedImage != "" && foundDockerfile == "" {
		plan.Provider = "compose"
		plan.DetectedFramework = "compose"
		plan.ComposeImage = detectedImage
		plan.DetectionSource = "layer0-compose-image"
		plan.DetectionConfidence = "high"
		if detectedCmd != "" {
			plan.StartCmd = detectedCmd
		} else {
			plan.StartCmd = "(defined in image)"
		}
		if detectedPort != 0 {
			plan.Port = detectedPort
		}
		if len(detectedEnv) > 0 {
			plan.Env = detectedEnv
		}
		if len(detectedVolumes) > 0 {
			plan.ComposeVolumes = detectedVolumes
		}
		return plan, nil
	}

	// 3b. Dockerfile exists (from root, subfolder, or compose reference)
	if foundDockerfile != "" {
		plan.Provider = "dockerfile"
		plan.DetectedFramework = "custom"
		plan.DockerfilePath = foundDockerfile
		plan.DetectionSource = "layer0-dockerfile"
		plan.DetectionConfidence = "high"
		if detectedCmd != "" {
			plan.StartCmd = detectedCmd
		} else {
			plan.StartCmd = "(defined in Dockerfile)"
		}

		if data, err := os.ReadFile(filepath.Join(projectDir, foundDockerfile)); err == nil {
			content := string(data)
			exposeRe := regexp.MustCompile(`(?m)^EXPOSE\s+(\d+)`)
			if matches := exposeRe.FindStringSubmatch(content); len(matches) > 1 {
				var expPort int
				fmt.Sscanf(matches[1], "%d", &expPort)
				if detectedPort == 0 {
					detectedPort = expPort
				}
			}
		}

		if detectedPort == 0 {
			detectedPort = detectConfigPort(projectDir)
		}
		if detectedPort != 0 {
			plan.Port = detectedPort
		}
		if len(detectedEnv) > 0 {
			plan.Env = detectedEnv
		}
		if len(detectedVolumes) > 0 {
			plan.ComposeVolumes = detectedVolumes
		}
		return plan, nil
	}

	return nil, fmt.Errorf("no Dockerfile or docker-compose found")
}

// detectConfigPort dynamically extracts port definitions from application config files (.env, config.toml, config.json, etc.)
func detectConfigPort(projectDir string) int {
	// 1. Check config*.toml (e.g. config.toml, config.example.toml, config.local.toml)
	tomlFiles, _ := filepath.Glob(filepath.Join(projectDir, "config*.toml"))
	for _, tf := range tomlFiles {
		if data, err := os.ReadFile(tf); err == nil {
			portRe := regexp.MustCompile(`(?m)^\s*port\s*=\s*(\d+)`)
			if matches := portRe.FindStringSubmatch(string(data)); len(matches) > 1 {
				var p int
				fmt.Sscanf(matches[1], "%d", &p)
				if p > 0 {
					return p
				}
			}
		}
	}

	// 2. Check .env / .env.example
	envFiles := []string{".env", ".env.example", ".env.local"}
	for _, ef := range envFiles {
		if data, err := os.ReadFile(filepath.Join(projectDir, ef)); err == nil {
			portRe := regexp.MustCompile(`(?m)^\s*(?:PORT|SERVER_PORT|APP_PORT)\s*=\s*(\d+)`)
			if matches := portRe.FindStringSubmatch(string(data)); len(matches) > 1 {
				var p int
				fmt.Sscanf(matches[1], "%d", &p)
				if p > 0 {
					return p
				}
			}
		}
	}

	return 0
}
