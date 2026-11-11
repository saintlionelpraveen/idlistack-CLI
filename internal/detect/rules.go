package detect

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/idlistack/cli/internal/app"
	"github.com/idlistack/cli/internal/buildplan"
	"github.com/idlistack/cli/internal/config"
)

type FrameworksConfig struct {
	Frameworks []FrameworkRule `json:"frameworks"`
}

type FrameworkRule struct {
	ID                 string            `json:"id"`
	Provider           string            `json:"provider"`
	Detect             DetectCondition   `json:"detect"`
	Port               int               `json:"port"`
	PreInstallCmd      string            `json:"pre_install_cmd,omitempty"`
	InstallCmd         string            `json:"install_cmd,omitempty"`
	BuildCmd           string            `json:"build_cmd,omitempty"`
	StartCmd           string            `json:"start_cmd,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	Dependencies       []string          `json:"dependencies,omitempty"`
	Volumes            []string          `json:"volumes,omitempty"`
	DockerfileTemplate string            `json:"dockerfile_template,omitempty"`
}

type DetectCondition struct {
	Files        []string `json:"files,omitempty"`
	Dirs         []string `json:"dirs,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

//go:embed frameworks.json
var defaultFrameworksJSON []byte

func detectWithRules(projectDir string, cfg *config.Config, verbose bool) (*buildplan.Plan, error) {
	var data []byte

	// 1. Load the dynamic rules file (can be placed in ~/.idlistack/ or embedded in binary)
	home, _ := os.UserHomeDir()
	rulesPath := filepath.Join(home, ".idlistack", "frameworks.json")
	
	if d, err := os.ReadFile(rulesPath); err == nil {
		data = d
	} else {
		data = defaultFrameworksJSON
	}

	var config FrameworksConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse frameworks.json: %w", err)
	}

	projectApp, err := app.NewApp(projectDir)
	if err != nil {
		return nil, err
	}

	// 2. Iterate over rules and check for a match
	for _, fw := range config.Frameworks {
		matched := false

		// Check files
		for _, file := range fw.Detect.Files {
			if projectApp.HasFile(file) {
				matched = true
				break
			}
		}

		// Check directories
		if !matched {
			for _, dir := range fw.Detect.Dirs {
				if projectApp.HasDir(dir) || projectApp.HasFile(dir) {
					matched = true
					break
				}
			}
		}

		// Check dependencies dynamically across multiple language manifests
		if !matched && len(fw.Detect.Dependencies) > 0 {
			// 1. Node.js (package.json)
			if projectApp.HasFile("package.json") {
				type PkgJson struct {
					Dependencies    map[string]string `json:"dependencies"`
					DevDependencies map[string]string `json:"devDependencies"`
				}
				var pkg PkgJson
				if err := projectApp.ReadJSON("package.json", &pkg); err == nil {
					for _, dep := range fw.Detect.Dependencies {
						if _, ok := pkg.Dependencies[dep]; ok {
							matched = true
							break
						}
						if _, ok := pkg.DevDependencies[dep]; ok {
							matched = true
							break
						}
					}
				}
			}

			// 2. Python (requirements.txt or pyproject.toml)
			if !matched && projectApp.HasFile("requirements.txt") {
				if content, err := projectApp.ReadFile("requirements.txt"); err == nil {
					for _, dep := range fw.Detect.Dependencies {
						if strings.Contains(strings.ToLower(string(content)), strings.ToLower(dep)) {
							matched = true
							break
						}
					}
				}
			}
			if !matched && projectApp.HasFile("pyproject.toml") {
				if content, err := projectApp.ReadFile("pyproject.toml"); err == nil {
					for _, dep := range fw.Detect.Dependencies {
						if strings.Contains(strings.ToLower(string(content)), strings.ToLower(dep)) {
							matched = true
							break
						}
					}
				}
			}

			// 3. Go (go.mod)
			if !matched && projectApp.HasFile("go.mod") {
				if content, err := projectApp.ReadFile("go.mod"); err == nil {
					for _, dep := range fw.Detect.Dependencies {
						if strings.Contains(string(content), dep) {
							matched = true
							break
						}
					}
				}
			}

			// 4. Rust (Cargo.toml)
			if !matched && projectApp.HasFile("Cargo.toml") {
				if content, err := projectApp.ReadFile("Cargo.toml"); err == nil {
					for _, dep := range fw.Detect.Dependencies {
						if strings.Contains(string(content), dep) {
							matched = true
							break
						}
					}
				}
			}
		}

		// 3. If matched, generate plan
		if matched {
			plan := buildplan.NewDefaultPlan()
			plan.Provider = fw.Provider
			plan.DetectedFramework = fw.ID
			plan.Port = fw.Port
			plan.PreInstallCmd = fw.PreInstallCmd
			plan.InstallCmd = fw.InstallCmd
			plan.BuildCmd = fw.BuildCmd
			plan.StartCmd = fw.StartCmd
			plan.Env = fw.Env
			plan.Dependencies = fw.Dependencies
			plan.Volumes = fw.Volumes
			plan.DetectionSource = "layer1.5-dynamic-rules"
			plan.DetectionConfidence = "high"

			// Handle custom dockerfile templates if defined in rules
			if fw.DockerfileTemplate != "" {
				version := "latest"
				var customApps []string
				isBench := false

				if fw.ID == "ghost" && projectApp.HasFile(".ghost-cli") {
					type GhostCli struct {
						ActiveVersion string `json:"active-version"`
					}
					var gcli GhostCli
					if err := projectApp.ReadJSON(".ghost-cli", &gcli); err == nil && gcli.ActiveVersion != "" {
						version = gcli.ActiveVersion
					}
				}

				if fw.ID == "frappe" {
					if projectApp.HasDir("apps") && projectApp.HasDir("sites") {
						isBench = true
						if content, err := projectApp.ReadFile("sites/apps.txt"); err == nil {
							lines := strings.Split(string(content), "\n")
							for _, line := range lines {
								line = strings.TrimSpace(line)
								if line != "" && line != "frappe" {
									customApps = append(customApps, line)
								}
							}
						}
					} else {
						customApps = append(customApps, filepath.Base(projectDir))
					}
				}

				data := struct {
					Version    string
					CustomApps []string
					IsBench    bool
				}{
					Version:    version,
					CustomApps: customApps,
					IsBench:    isBench,
				}

				// Fallback for simple string replacement if template parsing fails or it's a simple template
				content := fw.DockerfileTemplate
				if strings.Contains(content, "{{.") || strings.Contains(content, "{{range") || strings.Contains(content, "{{if") {
					tmpl, err := template.New("dockerfile").Parse(fw.DockerfileTemplate)
					if err == nil {
						var buf bytes.Buffer
						if err := tmpl.Execute(&buf, data); err == nil {
							content = buf.String()
						}
					}
				} else {
					content = strings.ReplaceAll(content, "{{version}}", version)
				}

				tmpDir := filepath.Join(os.TempDir(), "idlistack-build")
				_ = os.MkdirAll(tmpDir, 0755)
				outPath := filepath.Join(tmpDir, fmt.Sprintf("Dockerfile.%s.%s", filepath.Base(projectDir), fw.ID))
				_ = os.WriteFile(outPath, []byte(content), 0644)
				plan.DockerfilePath = outPath
			}

			plan.Runtime = detectRuntimeVersion(projectApp, fw.Provider)
			plan.FrameworkVersion = detectFrameworkVersion(projectApp, fw.ID)
			plan.Normalize()

			return plan, nil
		}
	}

	return nil, fmt.Errorf("no dynamic framework rule matched")
}

func detectRuntimeVersion(projectApp *app.App, provider string) string {
	switch provider {
	case "python":
		// 1. Check venv pyvenv.cfg
		for _, venvPath := range []string{"env/pyvenv.cfg", ".venv/pyvenv.cfg", "venv/pyvenv.cfg"} {
			if projectApp.HasFile(venvPath) {
				if content, err := projectApp.ReadFile(venvPath); err == nil {
					lines := strings.Split(string(content), "\n")
					for _, l := range lines {
						l = strings.TrimSpace(l)
						if strings.HasPrefix(l, "version_info =") || strings.HasPrefix(l, "version =") {
							parts := strings.SplitN(l, "=", 2)
							if len(parts) == 2 {
								return strings.TrimSpace(parts[1])
							}
						}
					}
				}
			}
		}
		// 2. Check .python-version
		if projectApp.HasFile(".python-version") {
			if content, err := projectApp.ReadFile(".python-version"); err == nil {
				v := strings.TrimSpace(string(content))
				if v != "" {
					return v
				}
			}
		}
		// 3. Check runtime.txt
		if projectApp.HasFile("runtime.txt") {
			if content, err := projectApp.ReadFile("runtime.txt"); err == nil {
				re := regexp.MustCompile(`python-(\d+\.\d+(\.\d+)?)`)
				if m := re.FindStringSubmatch(strings.TrimSpace(string(content))); len(m) > 1 {
					return m[1]
				}
			}
		}
		// 4. Check pyproject.toml
		if projectApp.HasFile("pyproject.toml") {
			if content, err := projectApp.ReadFile("pyproject.toml"); err == nil {
				re := regexp.MustCompile(`requires-python\s*=\s*["><=~^\s]*(\d+\.\d+)`)
				if m := re.FindStringSubmatch(string(content)); len(m) > 1 {
					return m[1]
				}
			}
		}
		// 5. Host python3 fallback
		if out, err := exec.Command("python3", "-V").Output(); err == nil {
			fields := strings.Fields(string(out))
			if len(fields) >= 2 {
				return fields[1]
			}
		}
		return "3"

	case "node":
		if projectApp.HasFile("package.json") {
			type pkg struct {
				Engines map[string]string `json:"engines"`
			}
			var p pkg
			if err := projectApp.ReadJSON("package.json", &p); err == nil && p.Engines != nil {
				if v, ok := p.Engines["node"]; ok {
					clean := strings.Trim(v, "^~>=< ")
					if clean != "" {
						return clean
					}
				}
			}
		}
		if projectApp.HasFile(".nvmrc") {
			if c, err := projectApp.ReadFile(".nvmrc"); err == nil {
				v := strings.TrimSpace(string(c))
				v = strings.TrimPrefix(v, "v")
				if v != "" {
					return v
				}
			}
		}
		if projectApp.HasFile(".node-version") {
			if c, err := projectApp.ReadFile(".node-version"); err == nil {
				v := strings.TrimSpace(string(c))
				v = strings.TrimPrefix(v, "v")
				if v != "" {
					return v
				}
			}
		}
		if out, err := exec.Command("node", "-v").Output(); err == nil {
			v := strings.TrimSpace(string(out))
			return strings.TrimPrefix(v, "v")
		}
		return "lts"

	case "go":
		if projectApp.HasFile("go.mod") {
			if c, err := projectApp.ReadFile("go.mod"); err == nil {
				re := regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(\.\d+)?)`)
				if m := re.FindStringSubmatch(string(c)); len(m) > 1 {
					return m[1]
				}
			}
		}
		if out, err := exec.Command("go", "version").Output(); err == nil {
			fields := strings.Fields(string(out))
			if len(fields) >= 3 && strings.HasPrefix(fields[2], "go") {
				return strings.TrimPrefix(fields[2], "go")
			}
		}
		return "1"

	case "rust":
		if projectApp.HasFile("Cargo.toml") {
			if c, err := projectApp.ReadFile("Cargo.toml"); err == nil {
				re := regexp.MustCompile(`(?:rust-version|edition)\s*=\s*"([^"]+)"`)
				if m := re.FindStringSubmatch(string(c)); len(m) > 1 {
					return m[1]
				}
			}
		}
		return "latest"
	}
	return ""
}

func detectFrameworkVersion(projectApp *app.App, frameworkID string) string {
	switch frameworkID {
	case "frappe":
		for _, fpath := range []string{"apps/frappe/frappe/__init__.py", "frappe/__init__.py"} {
			if projectApp.HasFile(fpath) {
				if c, err := projectApp.ReadFile(fpath); err == nil {
					re := regexp.MustCompile(`__version__\s*=\s*["']([^"']+)["']`)
					if m := re.FindStringSubmatch(string(c)); len(m) > 1 {
						return m[1]
					}
				}
			}
		}
		if projectApp.HasFile("apps/frappe/pyproject.toml") {
			if c, err := projectApp.ReadFile("apps/frappe/pyproject.toml"); err == nil {
				re := regexp.MustCompile(`version\s*=\s*["']([^"']+)["']`)
				if m := re.FindStringSubmatch(string(c)); len(m) > 1 {
					return m[1]
				}
			}
		}
	case "ghost":
		if projectApp.HasFile(".ghost-cli") {
			type GhostCli struct {
				ActiveVersion string `json:"active-version"`
			}
			var g GhostCli
			if err := projectApp.ReadJSON(".ghost-cli", &g); err == nil && g.ActiveVersion != "" {
				return g.ActiveVersion
			}
		}
	case "nextjs", "nuxt", "nestjs", "sveltekit":
		depMap := map[string]string{
			"nextjs":    "next",
			"nuxt":      "nuxt",
			"nestjs":    "@nestjs/core",
			"sveltekit": "@sveltejs/kit",
		}
		depName := depMap[frameworkID]
		if projectApp.HasFile("package.json") {
			type Pkg struct {
				Dependencies    map[string]string `json:"dependencies"`
				DevDependencies map[string]string `json:"devDependencies"`
			}
			var p Pkg
			if err := projectApp.ReadJSON("package.json", &p); err == nil {
				if v, ok := p.Dependencies[depName]; ok {
					return strings.Trim(v, "^~>=< ")
				}
				if v, ok := p.DevDependencies[depName]; ok {
					return strings.Trim(v, "^~>=< ")
				}
			}
		}
	case "django", "fastapi", "flask":
		depNames := map[string][]string{
			"django":  {"django", "Django"},
			"fastapi": {"fastapi", "FastAPI"},
			"flask":   {"flask", "Flask"},
		}
		for _, name := range depNames[frameworkID] {
			if projectApp.HasFile("requirements.txt") {
				if c, err := projectApp.ReadFile("requirements.txt"); err == nil {
					re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(name) + `[=~><^]*([\d\.]+)`)
					if m := re.FindStringSubmatch(string(c)); len(m) > 1 {
						return m[1]
					}
				}
			}
			if projectApp.HasFile("pyproject.toml") {
				if c, err := projectApp.ReadFile("pyproject.toml"); err == nil {
					re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(name) + `[=~><^"'\s]*([\d\.]+)`)
					if m := re.FindStringSubmatch(string(c)); len(m) > 1 {
						return m[1]
					}
				}
			}
		}
	}
	return ""
}
