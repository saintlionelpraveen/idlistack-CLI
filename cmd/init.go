package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/auth"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/detect"
	"github.com/idlistack/cli/internal/k8s"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new IdliStack project in the current directory",
	Long: `Initialize a new IdliStack project by creating an idlistack.toml config file
and a .idlistack.json file for project state.

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
	if cfg, err := config.Load(cwd); err == nil && !initForce {
		ui.Warn(fmt.Sprintf("Project %s already initialized (.idlistack.json exists). Use --force to re-initialize.", color.CyanString(cfg.Project.Name)))
		return nil
	}

	// Clean up any legacy directory & toml
	_ = os.RemoveAll(filepath.Join(cwd, ".idlistack"))
	_ = os.Remove(filepath.Join(cwd, "idlistack.toml"))

	// Dynamically determine target namespace from active kubecontext or fallback to project name
	targetNS := k8s.ResolveNamespace(cmd.Context(), "", projectName)

	// Create project state (.idlistack.json)
	projectState := &config.ProjectState{
		Version:     "1.0",
		ProjectName: projectName,
		ProjectDir:  cwd,
		Namespace:   targetNS,
		Project: config.ProjectConfig{
			Name: projectName,
		},
		Deploy: config.DeployConfig{
			Namespace:       targetNS,
			Replicas:        1,
			HealthCheckPath: "/health",
		},
	}

	// Attempt detection to auto-populate the config
	ui.Info("Detecting application for initial configuration...")
	plan, _ := detect.Detect(cmd.Context(), cwd, nil, false)
	if plan != nil {
		ui.Success(fmt.Sprintf("Detected %s application", color.CyanString(plan.Provider)))
		projectState.Plan = plan
		projectState.Build = config.BuildConfig{
			Provider:      plan.Provider,
			Runtime:       plan.Runtime,
			PreInstallCmd: plan.PreInstallCmd,
			InstallCmd:    plan.InstallCmd,
			BuildCmd:      plan.BuildCmd,
			StartCmd:      plan.StartCmd,
		}
		if plan.Port > 0 {
			projectState.Deploy.Port = plan.Port
		}
		if len(plan.Dependencies) > 0 {
			projectState.Deploy.Dependencies = plan.Dependencies
		}
	}

	if err := config.SaveProjectState(cwd, projectState); err != nil {
		return fmt.Errorf("failed to write %s: %w", config.ProjectStateFileName, err)
	}

	// Append to project .gitignore if it exists
	projectGitignore := filepath.Join(cwd, ".gitignore")
	if f, err := os.OpenFile(projectGitignore, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		defer f.Close()
		f.WriteString("\n# IdliStack\n.idlistack.json\n.idlistack/\nidlistack.toml\n")
	}

	if creds, _ := auth.LoadCredentials(); creds != nil && creds.User != "" {
		ui.Detail("Configured for deployment to namespace %s (User: %s)", color.CyanString(targetNS), color.CyanString(creds.User))
	}

	ui.Success(fmt.Sprintf("Project %s initialized!", color.CyanString(projectName)))
	fmt.Println()
	ui.Info("Created:")
	fmt.Printf("  %s  .idlistack.json  (project config & state, gitignored)\n", color.GreenString("→"))
	fmt.Println()
	ui.Info(fmt.Sprintf("Next: run %s to detect, build, and deploy", color.CyanString("idlistack up")))

	return nil
}

// EnsureProjectInitialized checks if .idlistack.json exists. If missing, it auto-initializes the project.
func EnsureProjectInitialized(ctx context.Context, cwd string) (*config.Config, error) {
	if cfg, err := config.Load(cwd); err == nil {
		return cfg, nil
	}

	projectName := filepath.Base(cwd)
	ui.Info(fmt.Sprintf("Auto-initializing project %s (zero configuration)...", color.CyanString(projectName)))

	// Clean up any legacy directory & toml
	_ = os.RemoveAll(filepath.Join(cwd, ".idlistack"))
	_ = os.Remove(filepath.Join(cwd, "idlistack.toml"))

	targetNS := k8s.ResolveNamespace(ctx, "", projectName)

	projectState := &config.ProjectState{
		Version:     "1.0",
		ProjectName: projectName,
		ProjectDir:  cwd,
		Namespace:   targetNS,
		Project: config.ProjectConfig{
			Name: projectName,
		},
		Deploy: config.DeployConfig{
			Namespace:       targetNS,
			Replicas:        1,
			HealthCheckPath: "/health",
		},
	}

	plan, _ := detect.Detect(ctx, cwd, nil, false)
	if plan != nil {
		projectState.Plan = plan
		projectState.Build = config.BuildConfig{
			Provider:      plan.Provider,
			Runtime:       plan.Runtime,
			PreInstallCmd: plan.PreInstallCmd,
			InstallCmd:    plan.InstallCmd,
			BuildCmd:      plan.BuildCmd,
			StartCmd:      plan.StartCmd,
		}
		if plan.Port > 0 {
			projectState.Deploy.Port = plan.Port
		}
		if len(plan.Dependencies) > 0 {
			projectState.Deploy.Dependencies = plan.Dependencies
		}
	}
	if err := config.SaveProjectState(cwd, projectState); err != nil {
		return nil, fmt.Errorf("failed to save project state: %w", err)
	}

	// Append to project .gitignore if it exists
	if f, err := os.OpenFile(filepath.Join(cwd, ".gitignore"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		defer f.Close()
		f.WriteString("\n# IdliStack\n.idlistack.json\n.idlistack/\nidlistack.toml\n")
	}

	ui.Success(fmt.Sprintf("✓ Initialized %s configuration", color.CyanString(config.ProjectStateFileName)))
	return projectState.ToConfig(), nil
}
