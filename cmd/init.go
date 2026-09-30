package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/auth"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/detect"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new IdliStack project in the current directory",
	Long: `Initialize a new IdliStack project by creating an idlistack.toml config file
and a .idlistack/ directory for internal state.

This links the current directory to an IdliStack project and sets up the
necessary configuration for detection, building, and deployment.`,
	RunE: runInit,
}

var (
	initProjectName string
	initForce       bool
)

func init() {
	initCmd.Flags().StringVarP(&initProjectName, "name", "n", "", "Project name (defaults to directory name)")
	initCmd.Flags().BoolVarP(&initForce, "force", "f", false, "Force re-initialization (overwrites existing idlistack.toml)")
}

func runInit(cmd *cobra.Command, args []string) error {
	ui.PrintBanner()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	projectName := initProjectName
	if projectName == "" {
		projectName = filepath.Base(cwd)
	}

	// Check if already initialized
	configPath := filepath.Join(cwd, "idlistack.toml")
	if _, err := os.Stat(configPath); err == nil && !initForce {
		ui.Warn("Project already initialized (idlistack.toml exists). Use --force to re-initialize.")
		return nil
	}

	// Create .idlistack directory
	idlistackDir := filepath.Join(cwd, ".idlistack")
	if err := os.MkdirAll(idlistackDir, 0755); err != nil {
		return fmt.Errorf("failed to create .idlistack directory: %w", err)
	}

	// Create link.json (internal state, gitignored)
	linkData := config.LinkConfig{
		ProjectName: projectName,
		ProjectDir:  cwd,
	}
	linkBytes, _ := json.MarshalIndent(linkData, "", "  ")
	linkPath := filepath.Join(idlistackDir, "link.json")
	if err := os.WriteFile(linkPath, linkBytes, 0600); err != nil {
		return fmt.Errorf("failed to write link.json: %w", err)
	}

	// Attempt detection to auto-populate the config
	ui.Info("Detecting application for initial configuration...")
	plan, _ := detect.Detect(cmd.Context(), cwd, nil, false)

	var buildSection, deploySection string
	if plan != nil {
		ui.Success(fmt.Sprintf("Detected %s application", color.CyanString(plan.Provider)))
		buildSection = fmt.Sprintf(`[build]
provider = "%s"`, plan.Provider)
		
		if plan.Runtime != "" {
			buildSection += fmt.Sprintf("\nruntime = \"%s\"", plan.Runtime)
		}
		if plan.PreInstallCmd != "" && len(plan.PreInstallCmd) < 80 {
			buildSection += fmt.Sprintf("\npre_install_cmd = %q", plan.PreInstallCmd)
		} else if plan.PreInstallCmd != "" {
			buildSection += "\n# pre_install_cmd = \"\" # override provider default if needed"
		}
		if plan.InstallCmd != "" && len(plan.InstallCmd) < 80 {
			buildSection += fmt.Sprintf("\ninstall_cmd = %q", plan.InstallCmd)
		}
		if plan.BuildCmd != "" {
			buildSection += fmt.Sprintf("\nbuild_cmd = %q", plan.BuildCmd)
		}
		if plan.StartCmd != "" && !strings.HasPrefix(plan.StartCmd, "/") {
			buildSection += fmt.Sprintf("\nstart_cmd = %q", plan.StartCmd)
		}

		deploySection = "[deploy]\n"
		if plan.Port > 0 {
			deploySection += fmt.Sprintf("port = %d\n", plan.Port)
		} else {
			deploySection += "# port = 0\n"
		}
		deploySection += "replicas = 1\nhealth_check_path = \"/health\""
	} else {
		ui.Warn("Could not automatically detect application, generating template config")
		buildSection = `# [build]
# provider = ""          # auto-detected if empty
# build_cmd = ""         # override the detected build command
# start_cmd = ""         # override the detected start command`
		deploySection = `# [deploy]
# port = 0               # auto-detected if empty
# replicas = 1
# health_check_path = "/health"`
	}

	// Create idlistack.toml
	tomlContent := fmt.Sprintf(`# IdliStack Configuration
# Documentation: https://idlistack.com/docs/config

[project]
name = "%s"

%s

%s

# [env]
# KEY = "value"
`, projectName, buildSection, deploySection)

	if err := os.WriteFile(configPath, []byte(tomlContent), 0644); err != nil {
		return fmt.Errorf("failed to write idlistack.toml: %w", err)
	}

	// Create .idlistack/.gitignore
	gitignoreContent := `# IdliStack internal state
link.json
buildplan.json
*.lock
`
	gitignorePath := filepath.Join(idlistackDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(gitignoreContent), 0644); err != nil {
		return fmt.Errorf("failed to write .gitignore: %w", err)
	}

	// Append to project .gitignore if it exists
	projectGitignore := filepath.Join(cwd, ".gitignore")
	if f, err := os.OpenFile(projectGitignore, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		defer f.Close()
		f.WriteString("\n# IdliStack\n.idlistack/\n")
	}

	targetNS := fmt.Sprintf("idlistack-%s", strings.ToLower(projectName))
	if creds, err := auth.VerifyAndAuthorize(cmd.Context(), targetNS); err == nil && creds != nil {
		ui.Detail("Bound namespace %s to Keycloak user %s (RBAC: %s)", color.CyanString(targetNS), color.CyanString(creds.Username), color.GreenString(creds.Scope))
	}

	ui.Success(fmt.Sprintf("Project %s initialized!", color.CyanString(projectName)))
	fmt.Println()
	ui.Info("Created:")
	fmt.Printf("  %s  idlistack.toml (project config)\n", color.GreenString("→"))
	fmt.Printf("  %s  .idlistack/    (internal state, gitignored)\n", color.GreenString("→"))
	fmt.Println()
	ui.Info(fmt.Sprintf("Next: run %s to detect, build, and deploy", color.CyanString("idlistack up")))

	return nil
}
