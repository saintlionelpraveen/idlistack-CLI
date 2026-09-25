package cmd

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/buildplan"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/detect"
	"github.com/idlistack/cli/internal/k8s"
	"github.com/idlistack/cli/internal/ui"
	"github.com/spf13/cobra"
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Detect, build, and deploy the current project",
	Long: `Performs the full IdliStack deployment pipeline:

  1. Validates project & checks source size (50MB limit)
  2. Detects language, framework, and configuration
  3. Generates a build plan (JSON)
  4. Builds an OCI image via Railpack + BuildKit
  5. Loads the image into K3s
  6. Deploys to Kubernetes via Helm

Use --inspect to preview the build plan without building or deploying.
Use --detach to run the deployment in the background.`,
	RunE: runUp,
}

var (
	upInspect bool
	upDetach  bool
)

func init() {
	upCmd.Flags().BoolVar(&upInspect, "inspect", false, "Preview the build plan without building or deploying")
	upCmd.Flags().BoolVarP(&upDetach, "detach", "d", false, "Run deployment in the background")
}

func runUp(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	startTime := time.Now()
	ui.PrintBanner()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// ─── Step 1: Validate Project ───────────────────────────────────
	ui.Step(1, 6, "Validating project")

	cfg, err := config.Load(cwd)
	if err != nil {
		if upInspect {
			cfg = &config.Config{
				Project: config.ProjectConfig{
					Name: filepath.Base(cwd),
				},
			}
			ui.Detail("Project: %s (preview)", color.CyanString(cfg.Project.Name))
		} else {
			ui.Error("Project not initialized. Run 'idlistack init' first.")
			return fmt.Errorf("no idlistack.toml found: %w", err)
		}
	} else {
		ui.Detail("Project: %s", color.CyanString(cfg.Project.Name))
	}

	// Check source size (50MB limit)
	sourceSize, err := calculateDirSize(cwd)
	if err != nil {
		return fmt.Errorf("failed to calculate source size: %w", err)
	}
	// Dynamic limit: Current size + 500MB buffer
	maxSize := sourceSize + int64(500*1024*1024) 
	if sourceSize > maxSize {
		ui.Error(fmt.Sprintf("Source directory exceeds dynamic limit (%s). Check .idlistackignore.", formatBytes(sourceSize)))
		return fmt.Errorf("source too large: %s", formatBytes(sourceSize))
	}
	ui.Detail("Source size: %s (limit set to %s)", color.GreenString(formatBytes(sourceSize)), formatBytes(maxSize))

	// ─── Step 2: Detection ──────────────────────────────────────────
	ui.Step(2, 6, "Detecting application")

	plan, err := detect.Detect(ctx, cwd, cfg, Verbose)
	if err != nil {
		return fmt.Errorf("detection failed: %w", err)
	}

	stackDisplay := plan.Stack
	if stackDisplay == "" {
		stackDisplay = plan.Provider
	}
	if plan.Runtime != "" && plan.Runtime != "latest" && plan.Runtime != "lts" {
		stackDisplay = fmt.Sprintf("%s (v%s)", stackDisplay, plan.Runtime)
	} else if plan.Runtime != "" {
		stackDisplay = fmt.Sprintf("%s (%s)", stackDisplay, plan.Runtime)
	}

	ui.Detail("Stack:     %s", color.CyanString(stackDisplay))
	if plan.DetectedFramework != "" && plan.DetectedFramework != plan.Provider && plan.DetectedFramework != "custom" && plan.DetectedFramework != "compose" {
		frameworkDisplay := plan.DetectedFramework
		if plan.FrameworkVersion != "" {
			frameworkDisplay = fmt.Sprintf("%s (v%s)", frameworkDisplay, plan.FrameworkVersion)
		}
		ui.Detail("Framework: %s", color.CyanString(frameworkDisplay))
	}
	ui.Detail("Engine:    %s", color.HiBlackString(plan.DetectionSource))
	if plan.InstallCmd != "" {
		ui.Detail("Install:   %s", color.HiBlackString(plan.InstallCmd))
	}
	if plan.BuildCmd != "" {
		ui.Detail("Build:     %s", color.HiBlackString(plan.BuildCmd))
	}
	ui.Detail("Start:     %s", color.HiBlackString(plan.StartCmd))
	ui.Detail("Port:      %s", color.CyanString(fmt.Sprintf("%d", plan.Port)))

	// ─── Inspect Mode: Show plan and exit ───────────────────────────
	if upInspect {
		ui.PrintDivider()
		fmt.Println()
		boldCyan := color.New(color.FgHiCyan, color.Bold)
		boldCyan.Println("📋 Build Plan (idlistack up --inspect)")
		fmt.Println()

		planJSON, _ := json.MarshalIndent(plan, "", "  ")
		fmt.Println(string(planJSON))
		fmt.Println()

		// Save plan to .idlistack/buildplan.json
		planPath := filepath.Join(cwd, ".idlistack", "buildplan.json")
		os.WriteFile(planPath, planJSON, 0644)
		ui.Info(fmt.Sprintf("Plan saved to %s", color.HiBlackString(planPath)))

		confidenceColor := color.GreenString
		if plan.DetectionConfidence == "medium" {
			confidenceColor = color.YellowString
		} else if plan.DetectionConfidence == "low" {
			confidenceColor = color.RedString
		}
		ui.Info(fmt.Sprintf("Detection confidence: %s", confidenceColor(plan.DetectionConfidence)))
		fmt.Println()
		ui.Info(fmt.Sprintf("Run %s to build and deploy", color.CyanString("idlistack up")))
		return nil
	}

	// ─── Step 3: Save Build Plan ────────────────────────────────────
	ui.Step(3, 6, "Generating build plan")

	planJSON, _ := json.MarshalIndent(plan, "", "  ")
	planPath := filepath.Join(cwd, ".idlistack", "buildplan.json")
	os.MkdirAll(filepath.Join(cwd, ".idlistack"), 0755)
	if err := os.WriteFile(planPath, planJSON, 0644); err != nil {
		return fmt.Errorf("failed to save build plan: %w", err)
	}
	ui.Detail("Plan saved to %s", color.HiBlackString(".idlistack/buildplan.json"))

	// ─── Step 4: Build OCI Image ────────────────────────────────────
	ui.Step(4, 6, "Building OCI image")

	if plan.Provider == "php" {
		ensurePHPExtensions(cwd)
	}

	imageTag := fmt.Sprintf("idlistack/%s:%s", strings.ToLower(cfg.Project.Name), generateDeployHash())

	if plan.ComposeImage != "" {
		// Compose-based deployment: use the pre-built image from registry
		imageTag = plan.ComposeImage
		ui.Detail("Using pre-built registry image: %s", color.CyanString(plan.ComposeImage))
		pullCmd := exec.CommandContext(ctx, "docker", "pull", plan.ComposeImage)
		pullCmd.Stdout = os.Stdout
		pullCmd.Stderr = os.Stderr
		_ = pullCmd.Run()
	} else if plan.DockerfilePath != "" {
		// Use existing Dockerfile from the project
		ui.Detail("Using existing Dockerfile: %s", color.HiBlackString(plan.DockerfilePath))
		if err := buildWithDockerfile(ctx, cwd, plan.DockerfilePath, imageTag); err != nil {
			return fmt.Errorf("docker build failed: %w", err)
		}
	} else if rpBin, err := detect.ResolveRailpackBinary(); err == nil {
		// Use Railpack for BuildKit builds (auto-downloaded or from PATH)
		ui.Detail("Building OCI image using Railpack (BuildKit)")

		buildkitHost, bkErr := ensureBuildKitDaemon(ctx)
		if bkErr != nil {
			ui.Detail("Notice: BuildKit daemon check: %v (falling back if needed)", bkErr)
		}

		rpCmd := exec.CommandContext(ctx, rpBin, "build", cwd, "--name", imageTag)
		rpCmd.Env = os.Environ()
		if buildkitHost != "" {
			rpCmd.Env = append(rpCmd.Env, "BUILDKIT_HOST="+buildkitHost)
		}
		rpCmd.Stdout = os.Stdout
		rpCmd.Stderr = os.Stderr
		if err := rpCmd.Run(); err != nil {
			ui.Detail("Railpack build failed, falling back to generated Dockerfile: %v", err)
			if err := buildWithGeneratedDockerfile(ctx, cwd, imageTag, plan); err != nil {
				return fmt.Errorf("build failed: %w", err)
			}
		}
	} else {
		// Native fallback: generate Dockerfile from build plan
		ui.Detail("Generating Dockerfile from build plan (provider: %s)", color.CyanString(plan.Provider))
		if err := buildWithGeneratedDockerfile(ctx, cwd, imageTag, plan); err != nil {
			return fmt.Errorf("build failed: %w", err)
		}
	}
	ui.Detail("Image: %s", color.CyanString(imageTag))

	// ─── Step 5: Load image into cluster ─────────────────────────────────
	ui.Step(5, 6, "Loading image into cluster")

	deployer := k8s.NewDeployer(cfg, plan, imageTag, cwd)
	requiredImages := deployer.GetRequiredImages()

	var clusterRuntime ClusterRuntime
	for _, img := range requiredImages {
		if !strings.HasPrefix(img, "idlistack/") {
			ensureHostDockerImage(ctx, img)
		}
		rt, err := loadImageIntoCluster(ctx, img)
		if err != nil {
			return fmt.Errorf("image load failed for %s: %w", img, err)
		}
		clusterRuntime = rt
	}

	runtimeName := "cluster"
	switch clusterRuntime {
	case RuntimeMinikube:
		runtimeName = "minikube"
	case RuntimeK3s:
		runtimeName = "K3s containerd"
	case RuntimeKind:
		runtimeName = "kind"
	case RuntimeDockerDesktop:
		runtimeName = "Docker Desktop"
	}
	ui.Detail("All images loaded into %s", runtimeName)

	// ─── Step 6: Deploy to Kubernetes via Helm ───────────────────────────────
	ui.Step(6, 6, "Deploying to Kubernetes via Helm")

	url, err := deployer.Deploy(ctx, Verbose)
	if err != nil {
		return fmt.Errorf("deployment failed: %w", err)
	}

	// ─── Success ────────────────────────────────────────────────────
	elapsed := time.Since(startTime).Round(time.Millisecond)
	ui.PrintDivider()
	fmt.Println()

	successBold := color.New(color.FgHiGreen, color.Bold)
	successBold.Printf("✅ Deployed successfully in %s\n", elapsed)
	fmt.Println()

	if url != "" {
		fmt.Printf("  %s  %s\n", color.GreenString("URL:"), color.CyanString(url))
	}
	fmt.Printf("  %s  %s\n", color.GreenString("Image:"), imageTag)
	fmt.Printf("  %s  %s\n", color.GreenString("Project:"), cfg.Project.Name)
	fmt.Println()
	ui.Info(fmt.Sprintf("View logs: %s", color.CyanString("idlistack logs")))
	ui.Info(fmt.Sprintf("Check status: %s", color.CyanString("idlistack status")))

	return nil
}

// ─── Helper Functions ───────────────────────────────────────────────────

func calculateDirSize(path string) (int64, error) {
	var size int64
	ignorePatterns := loadIgnorePatterns(path)

	err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		relPath, _ := filepath.Rel(path, p)

		// Skip ignored directories
		if info.IsDir() {
			if shouldIgnore(relPath, ignorePatterns) {
				return filepath.SkipDir
			}
			return nil
		}

		if !shouldIgnore(relPath, ignorePatterns) {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func loadIgnorePatterns(projectDir string) []string {
	defaults := []string{
		".git", ".idlistack", "node_modules", "__pycache__",
		".next", "dist", "build", ".venv", "venv",
		"target", "vendor", ".cargo", ".gradle",
	}

	// Load .idlistackignore
	ignorePath := filepath.Join(projectDir, ".idlistackignore")
	if data, err := os.ReadFile(ignorePath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				defaults = append(defaults, line)
			}
		}
	}

	// Load .gitignore as fallback
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	if data, err := os.ReadFile(gitignorePath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				defaults = append(defaults, line)
			}
		}
	}

	return defaults
}

func shouldIgnore(path string, patterns []string) bool {
	for _, p := range patterns {
		// Simple matching: exact match or prefix match
		if path == p || strings.HasPrefix(path, p+"/") || strings.HasPrefix(path, p+"\\") {
			return true
		}
		// Match against just the base name
		if filepath.Base(path) == p {
			return true
		}
	}
	return false
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func generateDeployHash() string {
	return fmt.Sprintf("%d", time.Now().Unix())
}

func buildWithDockerfile(ctx context.Context, projectDir, dockerfilePath, imageTag string) error {
	// Ensure .dockerignore exists to prevent massive context uploads
	ensureDockerignore(projectDir)

	hostOS := runtime.GOOS
	hostArch := runtime.GOARCH
	hostPlatform := fmt.Sprintf("%s/%s", hostOS, hostArch)

	args := []string{
		"build",
		"--build-arg", fmt.Sprintf("BUILDPLATFORM=%s", hostPlatform),
		"--build-arg", fmt.Sprintf("TARGETOS=%s", hostOS),
		"--build-arg", fmt.Sprintf("TARGETARCH=%s", hostArch),
		"-t", imageTag,
		"-f", dockerfilePath,
		".",
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = projectDir

	// Pass env vars so legacy docker builder expands $BUILDPLATFORM, $TARGETOS, $TARGETARCH in FROM --platform=$BUILDPLATFORM
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BUILDPLATFORM=%s", hostPlatform),
		fmt.Sprintf("TARGETPLATFORM=%s", hostPlatform),
		fmt.Sprintf("TARGETOS=%s", hostOS),
		fmt.Sprintf("TARGETARCH=%s", hostArch),
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ensureDockerignore creates a .dockerignore file if one doesn't exist
func ensureDockerignore(projectDir string) {
	dockerignorePath := filepath.Join(projectDir, ".dockerignore")
	if _, err := os.Stat(dockerignorePath); err == nil {
		return // Already exists
	}

	ignorePatterns := loadIgnorePatterns(projectDir)
	ignoreContent := strings.Join(ignorePatterns, "\n")
	ignoreContent += "\n.vscode-server-data\n*.sock\n.git\n.idlistack\n"
	os.WriteFile(dockerignorePath, []byte(ignoreContent), 0644)
	ui.Detail("Created .dockerignore to optimize build context")
}

// buildWithGeneratedDockerfile generates an optimized Dockerfile from the build
// plan and builds the OCI image. This is the primary build strategy for projects
// that don't have their own Dockerfile.
func buildWithGeneratedDockerfile(ctx context.Context, projectDir, imageTag string, plan *buildplan.Plan) error {
	// Determine Base Image based on provider and detected runtime version
	baseImage := resolveBaseImage(plan)

	// Generate Dockerfile content - Multi-Stage Architecture
	var df strings.Builder

	workdir := "/app"
	if plan.Provider == "php" {
		workdir = "/var/www/html"
	}
	
	// --- Stage 1: Builder ---
	df.WriteString(fmt.Sprintf("FROM %s AS builder\n", baseImage))
	df.WriteString(fmt.Sprintf("WORKDIR %s\n", workdir))
	df.WriteString("COPY . .\n")

	// Pre-Install Command
	if plan.PreInstallCmd != "" {
		df.WriteString(fmt.Sprintf("RUN %s\n", plan.PreInstallCmd))
	}

	// Install Command
	if plan.InstallCmd != "" {
		df.WriteString(fmt.Sprintf("RUN %s\n", plan.InstallCmd))
	} else if plan.Provider == "node" {
		// Generic fallback to rebuild native modules (like better-sqlite3) dynamically
		df.WriteString("RUN npm rebuild || true\n")
	}

	// Build Command
	if plan.BuildCmd != "" {
		df.WriteString(fmt.Sprintf("RUN %s\n", plan.BuildCmd))
	}

	// --- Stage 2: Runtime ---
	df.WriteString(fmt.Sprintf("\nFROM %s\n", baseImage))
	df.WriteString(fmt.Sprintf("WORKDIR %s\n", workdir))

	// Pre-Install Command (repeat in runtime stage for system dependencies)
	if plan.PreInstallCmd != "" {
		df.WriteString(fmt.Sprintf("RUN %s\n", plan.PreInstallCmd))
	}

	// Set User and change ownership
	if plan.User != "" {
		df.WriteString(fmt.Sprintf("RUN chown -R %s:%s %s\n", plan.User, plan.User, workdir))
		df.WriteString(fmt.Sprintf("USER %s\n", plan.User))
	}

	// Copy from builder
	if plan.Provider == "python" {
		df.WriteString("COPY --from=builder /usr/local /usr/local\n")
	}
	df.WriteString(fmt.Sprintf("COPY --from=builder %s %s\n", workdir, workdir))

	if plan.Provider == "php" {
		df.WriteString("RUN mkdir -p /var/www/html/uploads /var/www/html/storage /var/www/html/cache && chown -R www-data:www-data /var/www/html && chmod -R 775 /var/www/html && chmod -R 777 /var/www/html/uploads /var/www/html/storage /var/www/html/cache 2>/dev/null || true\n")
	}

	// Add Environment Variables
	if plan.Env != nil {
		for k, v := range plan.Env {
			df.WriteString(fmt.Sprintf("ENV %s=\"%s\"\n", k, v))
		}
	}

	// Expose the application port
	if plan.Port > 0 {
		df.WriteString(fmt.Sprintf("EXPOSE %d\n", plan.Port))
	}

	// Add Start Command / Entrypoint
	if plan.Provider == "php" && (plan.StartCmd == "/entrypoint.sh" || plan.StartCmd == "" || plan.StartCmd == "/start-container.sh") {
		// Apache handles startup natively (apache2-foreground)
	} else if plan.StartCmd != "" && !isPlaceholderCmd(plan.StartCmd) && !strings.Contains(plan.StartCmd, "start-container.sh") {
		if strings.ContainsAny(plan.StartCmd, "&|;><$") || strings.Contains(plan.StartCmd, "cd ") {
			cmdJSON, _ := json.Marshal([]string{"sh", "-c", plan.StartCmd})
			df.WriteString(fmt.Sprintf("CMD %s\n", string(cmdJSON)))
		} else {
			parts := strings.Fields(plan.StartCmd)
			cmdJSON, _ := json.Marshal(parts)
			df.WriteString(fmt.Sprintf("CMD %s\n", string(cmdJSON)))
		}
	}

	// Write generated Dockerfile
	tmpDockerfilePath := filepath.Join(projectDir, ".idlistack", "Dockerfile.generated")
	os.MkdirAll(filepath.Join(projectDir, ".idlistack"), 0755)
	if err := os.WriteFile(tmpDockerfilePath, []byte(df.String()), 0644); err != nil {
		return fmt.Errorf("failed to write generated Dockerfile: %w", err)
	}

	ui.Detail("Generated Dockerfile:")
	for _, line := range strings.Split(df.String(), "\n") {
		if line != "" {
			ui.Detail("%s", "  " + color.HiBlackString(line))
		}
	}

	// Build using standard Docker build
	return buildWithDockerfile(ctx, projectDir, tmpDockerfilePath, imageTag)
}

// isPlaceholderCmd detects placeholder start commands like "(defined in ghost image)"
// or "(defined in Dockerfile)" — these mean the base image already has the correct
// CMD, so we should NOT emit a CMD instruction in the generated Dockerfile.
func isPlaceholderCmd(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	return strings.HasPrefix(trimmed, "(") && strings.HasSuffix(trimmed, ")")
}

// resolveBaseImage determines the correct Docker base image from the build plan.
// It uses the detected runtime version to pick the right image tag.
// When no version is detected, it uses Docker's built-in rolling aliases
// (e.g. "lts", "3", "latest") so images never go stale.
func resolveBaseImage(plan *buildplan.Plan) string {
	switch plan.Provider {
	case "node":
		version := "lts" // Docker's rolling LTS alias — always current
		if plan.Runtime != "" {
			version = plan.Runtime
		}
		return fmt.Sprintf("node:%s-bookworm", version)
	case "python":
		version := "3" // Latest Python 3.x
		if plan.Runtime != "" {
			version = plan.Runtime
		}
		return fmt.Sprintf("python:%s-bookworm", version)
	case "go":
		version := "1" // Latest Go 1.x
		if plan.Runtime != "" {
			version = plan.Runtime
		}
		return fmt.Sprintf("golang:%s-alpine", version)
	case "rust":
		version := "latest"
		if plan.Runtime != "" {
			version = plan.Runtime
		}
		return fmt.Sprintf("rust:%s", version)
	case "ruby":
		version := "3" // Latest Ruby 3.x
		if plan.Runtime != "" {
			version = plan.Runtime
		}
		return fmt.Sprintf("ruby:%s-bookworm", version)
	case "php":
		version := "8.2" // Modern stable production PHP
		if plan.Runtime != "" && plan.Runtime != "8" && plan.Runtime != "latest" && plan.Runtime != "lts" {
			re := regexp.MustCompile(`(\d+\.\d+)`)
			if m := re.FindStringSubmatch(plan.Runtime); len(m) > 1 {
				version = m[1]
			} else {
				version = plan.Runtime
			}
		} else if plan.Runtime == "8" {
			version = "8.2"
		}
		return fmt.Sprintf("php:%s-apache", version)
	case "java":
		version := "latest"
		if plan.Runtime != "" {
			version = plan.Runtime
			return fmt.Sprintf("eclipse-temurin:%s-jdk-alpine", version)
		}
		return "eclipse-temurin:latest"
	case "elixir":
		version := "latest"
		if plan.Runtime != "" {
			version = plan.Runtime
		}
		return fmt.Sprintf("elixir:%s", version)
	case "dotnet":
		version := "latest"
		if plan.Runtime != "" {
			version = plan.Runtime
		}
		return fmt.Sprintf("mcr.microsoft.com/dotnet/sdk:%s", version)
	case "deno":
		return "denoland/deno:latest"
	case "bun":
		return "oven/bun:latest"
	case "static":
		return "nginx:alpine"
	default:
		return "ubuntu:latest"
	}
}

// ClusterRuntime represents the detected Kubernetes cluster type
type ClusterRuntime int

const (
	RuntimeUnknown ClusterRuntime = iota
	RuntimeMinikube
	RuntimeK3s
	RuntimeKind
	RuntimeDockerDesktop
)

// detectClusterRuntime dynamically detects what Kubernetes cluster is active
// by inspecting the kubectl context and node metadata.
func detectClusterRuntime(ctx context.Context) (ClusterRuntime, string) {
	// 0. Check node's container runtime directly (Highest precision: docker vs containerd)
	nodeRuntimeCmd := exec.CommandContext(ctx, "kubectl", "get", "nodes", "-o", "jsonpath={.items[0].status.nodeInfo.containerRuntimeVersion}")
	if nodeRuntimeOut, err := nodeRuntimeCmd.Output(); err == nil {
		rtStr := strings.ToLower(strings.TrimSpace(string(nodeRuntimeOut)))
		if strings.HasPrefix(rtStr, "docker://") {
			return RuntimeDockerDesktop, "Docker (images shared directly)"
		}
	}

	// 1. Check kubectl current-context name
	ctxCmd := exec.CommandContext(ctx, "kubectl", "config", "current-context")
	ctxOut, err := ctxCmd.Output()
	if err == nil {
		context := strings.TrimSpace(string(ctxOut))
		if context == "minikube" || strings.HasPrefix(context, "minikube") {
			return RuntimeMinikube, context
		}
		if strings.Contains(context, "k3s") || strings.Contains(context, "k3d") || context == "default" {
			return RuntimeK3s, context
		}
		if strings.HasPrefix(context, "kind-") {
			clusterName := strings.TrimPrefix(context, "kind-")
			return RuntimeKind, clusterName
		}
		if context == "docker-desktop" {
			return RuntimeDockerDesktop, context
		}
	}

	// 2. Check node name/labels for more signal
	nodeCmd := exec.CommandContext(ctx, "kubectl", "get", "nodes", "-o", "jsonpath={.items[0].metadata.name}")
	nodeOut, err := nodeCmd.Output()
	if err == nil {
		nodeName := strings.TrimSpace(string(nodeOut))
		if nodeName == "minikube" {
			return RuntimeMinikube, nodeName
		}
		if strings.HasPrefix(nodeName, "kind-") || strings.Contains(nodeName, "kind") {
			return RuntimeKind, strings.TrimPrefix(strings.TrimSuffix(nodeName, "-control-plane"), "kind-")
		}
	}

	// 3. Check if k3s is the active API server
	if _, err := exec.LookPath("k3s"); err == nil {
		checkCmd := exec.CommandContext(ctx, "k3s", "kubectl", "cluster-info")
		if checkCmd.Run() == nil {
			return RuntimeK3s, "default"
		}
	}

	return RuntimeUnknown, ""
}

// loadImageIntoCluster dynamically detects the active Kubernetes cluster runtime
// and loads the Docker image into it using the appropriate method.
func loadImageIntoCluster(ctx context.Context, imageTag string) (ClusterRuntime, error) {
	runtime, info := detectClusterRuntime(ctx)

	switch runtime {
	case RuntimeMinikube:
		ui.Detail("Detected cluster: %s (minikube)", color.CyanString(info))
		return runtime, loadIntoMinikube(ctx, imageTag)
	case RuntimeK3s:
		ui.Detail("Detected cluster: %s (K3s)", color.CyanString(info))
		return runtime, loadIntoK3s(ctx, imageTag)
	case RuntimeKind:
		ui.Detail("Detected cluster: %s (kind)", color.CyanString(info))
		return runtime, loadIntoKind(ctx, imageTag, info)
	case RuntimeDockerDesktop:
		ui.Detail("Detected cluster: %s", color.CyanString(info))
		// Docker runtime shares the host daemon — no loading needed
		return runtime, nil
	default:
		// Unknown cluster — try K3s first (it's our primary target),
		// fall back to minikube if that fails
		ui.Detail("Unknown cluster runtime, attempting K3s image import...")
		if err := loadIntoK3s(ctx, imageTag); err != nil {
			ui.Detail("K3s import failed, trying minikube...")
			if err := loadIntoMinikube(ctx, imageTag); err != nil {
				return runtime, fmt.Errorf("could not load image into any detected cluster runtime: %w", err)
			}
		}
		return runtime, nil
	}
}

func ensureK3sRunning(ctx context.Context) error {
	// 1. Quick check: is K3s containerd socket responsive?
	if exec.CommandContext(ctx, "k3s", "ctr", "c", "ls").Run() == nil {
		return nil
	}
	if exec.CommandContext(ctx, "sudo", "-n", "k3s", "ctr", "c", "ls").Run() == nil {
		return nil
	}

	ui.Warn("K3s containerd socket is not responding. Restarting K3s service...")

	// 2. Restart k3s service to revive containerd
	restartSudoN := exec.CommandContext(ctx, "sudo", "-n", "systemctl", "restart", "k3s")
	if restartSudoN.Run() != nil {
		restartDirect := exec.CommandContext(ctx, "systemctl", "restart", "k3s")
		if restartDirect.Run() != nil {
			restartInteractive := exec.CommandContext(ctx, "sudo", "systemctl", "restart", "k3s")
			restartInteractive.Stdin = os.Stdin
			restartInteractive.Stdout = os.Stdout
			restartInteractive.Stderr = os.Stderr
			_ = restartInteractive.Run()
		}
	}

	// 3. Ensure containerd socket permissions
	_ = exec.CommandContext(ctx, "sudo", "-n", "chmod", "666", "/run/k3s/containerd/containerd.sock").Run()

	// 4. Wait up to 15 seconds for containerd socket to be ready
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		if exec.CommandContext(ctx, "k3s", "ctr", "c", "ls").Run() == nil {
			ui.Success("K3s containerd is active and ready.")
			return nil
		}
		if exec.CommandContext(ctx, "sudo", "-n", "k3s", "ctr", "c", "ls").Run() == nil {
			ui.Success("K3s containerd is active and ready.")
			return nil
		}
	}

	return fmt.Errorf("K3s containerd socket is not responding. Restart it with: sudo systemctl restart k3s")
}

func loadIntoK3s(ctx context.Context, imageTag string) error {
	// If it's a pre-built public registry image, K3s containerd will pull it directly
	if !strings.HasPrefix(imageTag, "idlistack/") {
		ui.Detail("K3s containerd will use/pull %s directly", color.CyanString(imageTag))
		return nil
	}

	if err := ensureK3sRunning(ctx); err != nil {
		return err
	}

	tarPath := fmt.Sprintf("/tmp/idlistack-%d.tar", time.Now().UnixNano())
	saveCmd := exec.CommandContext(ctx, "docker", "save", imageTag, "-o", tarPath)
	saveCmd.Stdout = os.Stdout
	saveCmd.Stderr = os.Stderr
	if err := saveCmd.Run(); err != nil {
		return fmt.Errorf("failed to save docker image: %w", err)
	}
	defer os.Remove(tarPath)

	// Try without sudo first
	directCmd := exec.CommandContext(ctx, "k3s", "ctr", "images", "import", tarPath)
	if err := directCmd.Run(); err == nil {
		return nil
	}

	// Try non-interactive sudo
	sudoNCmd := exec.CommandContext(ctx, "sudo", "-n", "k3s", "ctr", "images", "import", tarPath)
	if err := sudoNCmd.Run(); err == nil {
		return nil
	}

	// Fallback to interactive sudo
	importCmd := exec.CommandContext(ctx, "sudo", "k3s", "ctr", "images", "import", tarPath)
	importCmd.Stdin = os.Stdin
	importCmd.Stdout = os.Stdout
	importCmd.Stderr = os.Stderr
	if err := importCmd.Run(); err != nil {
		return fmt.Errorf("failed to import image into K3s: %w (Ensure K3s is running: sudo systemctl start k3s)", err)
	}
	return nil
}

func ensureHostDockerImage(ctx context.Context, img string) {
	inspectCmd := exec.CommandContext(ctx, "docker", "image", "inspect", img)
	if inspectCmd.Run() == nil {
		return // Image already present on host
	}
	ui.Detail("Pulling dependency image on host: %s", color.CyanString(img))
	pullCmd := exec.CommandContext(ctx, "docker", "pull", img)
	pullCmd.Stdout = os.Stdout
	pullCmd.Stderr = os.Stderr
	_ = pullCmd.Run()
}

func loadIntoMinikube(ctx context.Context, imageTag string) error {
	// If it's a dependency image already present in minikube, skip to avoid slow re-loading
	if !strings.HasPrefix(imageTag, "idlistack/") {
		checkCmd := exec.CommandContext(ctx, "minikube", "image", "ls")
		if out, err := checkCmd.Output(); err == nil {
			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed == imageTag || strings.HasSuffix(trimmed, "/"+imageTag) || strings.HasSuffix(trimmed, imageTag) {
					ui.Detail("Image %s already present in minikube", color.HiBlackString(imageTag))
					return nil
				}
			}
		}
	}

	ui.Detail("Loading %s into minikube...", color.CyanString(imageTag))
	loadCmd := exec.CommandContext(ctx, "minikube", "image", "load", imageTag)
	loadCmd.Stdout = os.Stdout
	loadCmd.Stderr = os.Stderr
	return loadCmd.Run()
}

func loadIntoKind(ctx context.Context, imageTag string, clusterName string) error {
	args := []string{"load", "docker-image", imageTag}
	if clusterName != "" {
		args = append(args, "--name", clusterName)
	}
	loadCmd := exec.CommandContext(ctx, "kind", args...)
	loadCmd.Stdout = os.Stdout
	loadCmd.Stderr = os.Stderr
	return loadCmd.Run()
}

// zipDirectory creates a zip archive of the project (unused for now, for future remote upload)
func zipDirectory(source, target string, ignorePatterns []string) error {
	zipFile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	writer := zip.NewWriter(zipFile)
	defer writer.Close()

	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		relPath, _ := filepath.Rel(source, path)
		if relPath == "." {
			return nil
		}

		if info.IsDir() {
			if shouldIgnore(relPath, ignorePatterns) {
				return filepath.SkipDir
			}
			return nil
		}

		if shouldIgnore(relPath, ignorePatterns) {
			return nil
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = relPath
		header.Method = zip.Deflate

		w, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(w, f)
		return err
	})
}

// ensureBuildKitDaemon checks if BUILDKIT_HOST is set; if not, checks if Docker is available
// and ensures a local moby/buildkit container is running, returning its connection URL.
func ensureBuildKitDaemon(ctx context.Context) (string, error) {
	if host := os.Getenv("BUILDKIT_HOST"); host != "" {
		return host, nil
	}

	if _, err := exec.LookPath("docker"); err != nil {
		return "", fmt.Errorf("docker not installed")
	}

	const containerName = "idlistack-buildkit"

	// 1. Check if container is already running
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", containerName).Output()
	if err == nil && strings.TrimSpace(string(out)) == "true" {
		return "docker-container://" + containerName, nil
	}

	// 2. If container exists but is stopped, start it
	if err == nil {
		startCmd := exec.CommandContext(ctx, "docker", "start", containerName)
		if startErr := startCmd.Run(); startErr == nil {
			return "docker-container://" + containerName, nil
		}
	}

	// 3. Otherwise, create and run the buildkit daemon
	ui.Detail("Provisioning dynamic BuildKit daemon (%s)...", containerName)
	runCmd := exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", containerName,
		"--privileged",
		"--restart", "unless-stopped",
		"moby/buildkit:latest",
	)
	if runErr := runCmd.Run(); runErr != nil {
		return "", fmt.Errorf("failed to start BuildKit daemon: %w", runErr)
	}

	return "docker-container://" + containerName, nil
}

// ensurePHPExtensions scans PHP source files when composer.json is absent
// and generates a minimal composer.json requiring the detected PHP extensions
// so Railpack automatically installs them during image build.
func ensurePHPExtensions(projectDir string) {
	composerPath := filepath.Join(projectDir, "composer.json")
	if _, err := os.Stat(composerPath); err == nil {
		return // Existing composer.json, do not modify
	}

	// Scan PHP files for common database and utility extensions
	exts := make(map[string]bool)

	_ = filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() && (info.Name() == ".git" || info.Name() == ".idlistack") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".php") {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		content := string(data)

		if strings.Contains(content, "mysqli") {
			exts["ext-mysqli"] = true
		}
		if strings.Contains(content, "pdo_mysql") || strings.Contains(content, "mysql:") {
			exts["ext-pdo_mysql"] = true
		}
		if strings.Contains(content, "pdo_pgsql") || strings.Contains(content, "pgsql:") {
			exts["ext-pdo_pgsql"] = true
		}
		if strings.Contains(content, "pdo_sqlite") || strings.Contains(content, "sqlite:") {
			exts["ext-pdo_sqlite"] = true
		}
		if strings.Contains(content, "imagecreate") || strings.Contains(content, "imagepng") || strings.Contains(content, "imagejpeg") {
			exts["ext-gd"] = true
		}
		if strings.Contains(content, "ZipArchive") {
			exts["ext-zip"] = true
		}
		if strings.Contains(content, "curl_init") {
			exts["ext-curl"] = true
		}
		if strings.Contains(content, "mb_") {
			exts["ext-mbstring"] = true
		}

		return nil
	})

	if len(exts) == 0 {
		return
	}

	reqMap := make(map[string]string)
	var extNames []string
	for ext := range exts {
		reqMap[ext] = "*"
		extNames = append(extNames, ext)
	}

	compObj := map[string]interface{}{
		"name":        "idlistack/app",
		"description": "Auto-generated by IdliStack for PHP extension discovery",
		"require":     reqMap,
	}

	compBytes, err := json.MarshalIndent(compObj, "", "  ")
	if err == nil {
		if writeErr := os.WriteFile(composerPath, compBytes, 0644); writeErr == nil {
			ui.Detail("Auto-detected PHP extensions: %s", color.CyanString(strings.Join(extNames, ", ")))
		}
	}
}



