package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/buildplan"
	"github.com/idlistack/cli/internal/config"
	"github.com/idlistack/cli/internal/ui"
)

// Deployer manages Kubernetes deployments for IdliStack projects
type Deployer struct {
	config     *config.Config
	plan       *buildplan.Plan
	imageTag   string
	namespace  string
	appName    string // RFC 1123 compliant lowercase name for all K8s resources
	projectDir string
	kubeToken  string
}

// NewDeployer creates a new Deployer instance
func NewDeployer(cfg *config.Config, plan *buildplan.Plan, imageTag string, projectDir string) *Deployer {
	sanitized := sanitizeK8sName(cfg.Project.Name)
	return &Deployer{
		config:     cfg,
		plan:       plan,
		imageTag:   imageTag,
		namespace:  fmt.Sprintf("idlistack-%s", sanitized),
		appName:    sanitized,
		projectDir: projectDir,
	}
}

// sanitizeK8sName converts a project name to a valid RFC 1123 DNS label.
// Must be lowercase alphanumeric, may contain '-' or '.', and must start/end
// with an alphanumeric character.
func sanitizeK8sName(name string) string {
	// Lowercase
	s := strings.ToLower(name)
	// Replace underscores and spaces with hyphens
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	// Remove any characters that aren't alphanumeric, hyphen, or dot
	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
			result = append(result, c)
		}
	}
	// Trim leading/trailing hyphens and dots
	s = strings.Trim(string(result), "-.")
	if s == "" {
		s = "app"
	}
	return s
}

// Deploy executes the full deployment pipeline
func (d *Deployer) Deploy(ctx context.Context, verbose bool) (string, error) {
	// ─── Deploy Lock ────────────────────────────────────────────────
	lockPath := fmt.Sprintf("/tmp/idlistack-%s.lock", d.appName)
	lockFile, err := acquireLock(lockPath)
	if err != nil {
		return "", fmt.Errorf("deploy lock: another deploy is in progress for this project")
	}
	defer releaseLock(lockFile, lockPath)

	// ─── Initialize Helm Directory ──────────────────────────────────
	helmDir := filepath.Join(d.projectDir, ".idlistack", "helm")
	os.RemoveAll(helmDir)
	os.MkdirAll(filepath.Join(helmDir, "templates"), 0755)

	chartYaml := fmt.Sprintf("apiVersion: v2\nname: %s\ndescription: Idlistack App\nversion: 0.1.0\nappVersion: 1.0.0\n", d.appName)
	os.WriteFile(filepath.Join(helmDir, "Chart.yaml"), []byte(chartYaml), 0644)

	// ─── Namespace Manager ──────────────────────────────────────────
	if err := d.ensureNamespace(ctx); err != nil {
		return "", err
	}

	// ─── Verify Keycloak Auth & Bind K3s RBAC (Pre-Helm Gate) ───────
	k8sToken, kcUser, rbacScope, err := VerifyDeployAuthAndGetToken(ctx, d.namespace)
	if err != nil {
		return "", fmt.Errorf("Keycloak RBAC gate blocked Helm deploy: %w", err)
	}
	d.kubeToken = k8sToken
	ui.Detail("Keycloak RBAC verified: user=%s, scope=%s, namespace=%s", color.CyanString(kcUser), color.GreenString(rbacScope), d.namespace)

	// ─── Clean up previous failed deployments ───────────────────────
	d.cleanupOldDeployment(ctx)

	// ─── Ensure Dependency Services (Postgres, Redis, etc.) ─────────
	if err := d.ensureDependencies(ctx); err != nil {
		return "", fmt.Errorf("dependency provisioning failed: %w", err)
	}

	// ─── Pre-deploy jobs ────────────────────────────────────────────
	if d.plan.PreDeployCmd != "" {
		ui.Detail("Scaffolding pre-deploy job: %s", d.plan.PreDeployCmd)
		if err := d.runPreDeploy(ctx); err != nil {
			return "", fmt.Errorf("pre-deploy failed: %w", err)
		}
	}

	// ─── Apply env secrets ──────────────────────────────────────────
	secretName := fmt.Sprintf("%s-env", d.appName)
	hasSecrets := d.checkSecretsExist(ctx, secretName)

	// ─── Generate manifests ─────────────────────────────────────────
	ui.Detail("Generating Helm templates")
	if err := d.applyManifests(ctx, hasSecrets, secretName); err != nil {
		return "", fmt.Errorf("manifest generation failed: %w", err)
	}

	// ─── Helm Deploy ────────────────────────────────────────────────
	ui.Detail("Deploying via Helm (waiting for rollout to complete...)")
	if err := d.executeHelmDeploy(ctx); err != nil {
		// Show pod logs before rollback so user can see the crash reason
		ui.Warn("Deployment failed, fetching pod logs for debugging...")
		d.showPodLogs(ctx)

		ui.Warn("Attempting rollback...")
		d.rollback(ctx)
		return "", fmt.Errorf("deployment failed (rolled back): %w", err)
	}

	// ─── Auto-Migrate Local Data (if applicable) ────────────────────
	d.syncLocalDockerData(ctx)

	// ─── Get URL ────────────────────────────────────────────────────
	url := d.getServiceURL(ctx)

	return url, nil
}

// ─── Namespace ──────────────────────────────────────────────────────────

func (d *Deployer) ensureNamespace(ctx context.Context) error {
	// Check if namespace exists and if it is in Terminating state
	checkCmd := exec.CommandContext(ctx, "kubectl", "get", "namespace", d.namespace, "-o", "jsonpath={.status.phase}")
	phaseBytes, err := checkCmd.Output()
	if err != nil {
		// Namespace doesn't exist yet; Helm will create it with --create-namespace
		return nil
	}

	phase := strings.TrimSpace(string(phaseBytes))
	if phase == "Terminating" {
		ui.Warn(fmt.Sprintf("Namespace %s is in Terminating state. Waiting for cleanup to complete...", d.namespace))

		deadline := time.Now().Add(45 * time.Second)
		forced := false
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)

			// Check if namespace is deleted
			chk := exec.CommandContext(ctx, "kubectl", "get", "namespace", d.namespace)
			if err := chk.Run(); err != nil {
				ui.Success(fmt.Sprintf("Namespace %s cleaned up successfully", d.namespace))
				return nil
			}

			// If still terminating after 6 seconds, force-terminate stuck pods and PVCs
			if !forced && time.Now().After(deadline.Add(-39*time.Second)) {
				forced = true
				ui.Detail("Forcing termination of stuck resources in namespace %s...", d.namespace)
				exec.CommandContext(ctx, "kubectl", "delete", "pods", "--all", "-n", d.namespace, "--force", "--grace-period=0").Run()
				exec.CommandContext(ctx, "kubectl", "delete", "pvc", "--all", "-n", d.namespace, "--force", "--grace-period=0").Run()
				finalizeCmd := fmt.Sprintf("kubectl get ns %s -o json | jq '.spec.finalizers = []' | kubectl replace --raw /api/v1/namespaces/%s/finalize -f - 2>/dev/null || true", d.namespace, d.namespace)
				exec.CommandContext(ctx, "bash", "-c", finalizeCmd).Run()
			}
		}

		return fmt.Errorf("namespace %s is stuck in Terminating state; please check with 'kubectl get ns %s -o yaml'", d.namespace, d.namespace)
	}

	return nil
}

// ─── Manifests ──────────────────────────────────────────────────────────

func (d *Deployer) applyManifests(ctx context.Context, hasSecrets bool, secretName string) error {
	replicas := d.config.Deploy.Replicas
	if replicas == 0 {
		replicas = 1
	}

	port := d.plan.Port
	if d.config.Deploy.Port != 0 {
		port = d.config.Deploy.Port
	}

	// ─── Deterministic NodePort & URL Pre-allocation ────────────────
	nodePort := d.allocateNodePort(ctx)
	nodeIp := d.getNodeIP(ctx)
	appUrl := fmt.Sprintf("http://%s:%d", nodeIp, nodePort)

	// ─── Service manifest ───────────────────────────────────────────
	serviceManifest := fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    managed-by: idlistack
spec:
  type: NodePort
  selector:
    app: %s
  ports:
  - protocol: TCP
    port: %d
    targetPort: %d
    nodePort: %d
`,
		d.appName, d.namespace,
		d.appName,
		d.appName,
		port, port, nodePort,
	)

	if err := d.writeManifest("service", serviceManifest); err != nil {
		return fmt.Errorf("service template failed: %w", err)
	}

	// ─── Deployment manifest ────────────────────────────────────────
	envFromSection := ""
	if hasSecrets {
		envFromSection = fmt.Sprintf(`
          envFrom:
          - secretRef:
              name: %s`, secretName)
	}

	// Generate inline env vars from plan and provisioned dependencies
	envSection := ""
	dbUser, dbPass, dbName := d.extractDbCredentials()
	needsPostgres, needsMysql, needsRedis, _, _ := d.detectDependencies()

	containerEnv := make(map[string]string)
	containerEnv["APP_URL"] = appUrl

	if needsMysql {
		containerEnv["DB_HOST"] = "db"
		containerEnv["DB_PORT"] = "3306"
		containerEnv["DB_USER"] = dbUser
		containerEnv["DB_PASS"] = dbPass
		containerEnv["DB_PASSWORD"] = dbPass
		containerEnv["DB_NAME"] = dbName
		containerEnv["MYSQL_HOST"] = "db"
		containerEnv["MYSQL_PORT"] = "3306"
		containerEnv["MYSQL_USER"] = dbUser
		containerEnv["MYSQL_PASSWORD"] = dbPass
		containerEnv["MYSQL_DATABASE"] = dbName
	} else if needsPostgres {
		containerEnv["DB_HOST"] = "db"
		containerEnv["DB_PORT"] = "5432"
		containerEnv["DB_USER"] = dbUser
		containerEnv["DB_PASS"] = dbPass
		containerEnv["DB_PASSWORD"] = dbPass
		containerEnv["DB_NAME"] = dbName
		containerEnv["POSTGRES_HOST"] = "db"
		containerEnv["POSTGRES_PORT"] = "5432"
		containerEnv["POSTGRES_USER"] = dbUser
		containerEnv["POSTGRES_PASSWORD"] = dbPass
		containerEnv["POSTGRES_DB"] = dbName
		containerEnv["PGHOST"] = "db"
		containerEnv["PGPORT"] = "5432"
		containerEnv["PGUSER"] = dbUser
		containerEnv["PGPASSWORD"] = dbPass
		containerEnv["PGDATABASE"] = dbName
	}
	if needsRedis {
		containerEnv["REDIS_HOST"] = "redis"
		containerEnv["REDIS_PORT"] = "6379"
	}

	cleanAppUrl := strings.TrimSuffix(appUrl, "/")
	localhostRegex := regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1)(?::\d+)?(/?)`)

	for k, v := range d.plan.Env {
		v = strings.ReplaceAll(v, "{{DB_USER}}", dbUser)
		v = strings.ReplaceAll(v, "{{DB_PASS}}", dbPass)
		v = strings.ReplaceAll(v, "{{DB_NAME}}", dbName)
		v = strings.ReplaceAll(v, "{{DB_HOST}}", "db")
		if needsPostgres {
			v = strings.ReplaceAll(v, "{{DB_PORT}}", "5432")
		} else {
			v = strings.ReplaceAll(v, "{{DB_PORT}}", "3306")
		}
		v = strings.ReplaceAll(v, "{{APP_URL}}", appUrl)

		// Dynamically replace hardcoded localhost / 127.0.0.1 URLs with real NodePort appUrl
		v = localhostRegex.ReplaceAllStringFunc(v, func(match string) string {
			if strings.HasSuffix(match, "/") {
				return cleanAppUrl + "/"
			}
			return cleanAppUrl
		})

		containerEnv[k] = v
	}

	if len(containerEnv) > 0 {
		envSection = "\n        env:"
		for k, v := range containerEnv {
			envSection += fmt.Sprintf("\n        - name: %s\n          value: \"%s\"", k, v)
		}
	}

	// ─── Generate volume mounts and volumes ─────────────────────────
	type volumeDef struct {
		Name      string
		MountPath string
		HostPath  string
		IsPVC     bool
	}

	var volDefs []volumeDef
	seenMounts := make(map[string]bool)

	// 1. Process d.plan.ComposeVolumes (e.g. "./files:/app/vikunja/files", "./db:/db")
	for _, cv := range d.plan.ComposeVolumes {
		cv = strings.TrimSpace(cv)
		if cv == "" {
			continue
		}
		parts := strings.Split(cv, ":")
		var hostPart, containerPart string
		if len(parts) == 1 {
			containerPart = parts[0]
		} else {
			hostPart = parts[0]
			containerPart = parts[1]
		}
		containerPart = filepath.Clean(containerPart)
		if seenMounts[containerPart] {
			continue
		}
		seenMounts[containerPart] = true

		idx := len(volDefs)
		volName := fmt.Sprintf("data-%d", idx)

		var absHost string
		isHost := false
		if hostPart != "" {
			if strings.HasPrefix(hostPart, ".") || strings.HasPrefix(hostPart, "/") || strings.HasPrefix(hostPart, "~") {
				isHost = true
				if strings.HasPrefix(hostPart, "~") {
					home, _ := os.UserHomeDir()
					absHost = filepath.Join(home, strings.TrimPrefix(hostPart, "~"))
				} else if filepath.IsAbs(hostPart) {
					absHost = filepath.Clean(hostPart)
				} else {
					absHost = filepath.Clean(filepath.Join(d.projectDir, hostPart))
				}
				_ = os.MkdirAll(absHost, 0777)
			}
		}

		volDefs = append(volDefs, volumeDef{
			Name:      volName,
			MountPath: containerPart,
			HostPath:  absHost,
			IsPVC:     !isHost,
		})
	}

	// 2. Process d.plan.Volumes (e.g. from Ghost or other detected plans)
	for _, v := range d.plan.Volumes {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		cleanV := filepath.Clean(v)
		if seenMounts[cleanV] {
			continue
		}
		seenMounts[cleanV] = true

		idx := len(volDefs)
		volName := fmt.Sprintf("data-%d", idx)
		volDefs = append(volDefs, volumeDef{
			Name:      volName,
			MountPath: cleanV,
			IsPVC:     true,
		})
	}

	volumeMountsSection := ""
	volumesSection := ""
	pvcManifests := ""
	initContainersSection := ""

	if len(volDefs) > 0 {
		var vMounts strings.Builder
		vMounts.WriteString("\n        volumeMounts:")
		for _, vd := range volDefs {
			vMounts.WriteString(fmt.Sprintf("\n        - name: %s\n          mountPath: %s", vd.Name, vd.MountPath))
		}
		volumeMountsSection = vMounts.String()

		var vList strings.Builder
		vList.WriteString("\n      volumes:")
		for _, vd := range volDefs {
			if vd.HostPath != "" {
				vList.WriteString(fmt.Sprintf(`
      - name: %s
        hostPath:
          path: %s
          type: DirectoryOrCreate`, vd.Name, vd.HostPath))
			} else {
				claimName := fmt.Sprintf("%s-%s", d.appName, vd.Name)
				vList.WriteString(fmt.Sprintf(`
      - name: %s
        persistentVolumeClaim:
          claimName: %s`, vd.Name, claimName))
				pvcManifests += fmt.Sprintf(`---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %s
  namespace: %s
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 5Gi
`, claimName, d.namespace)
			}
		}
		volumesSection = vList.String()

		// InitContainer to guarantee file permissions for non-root containers (UID 1000, 33, 999, etc.)
		var initMounts strings.Builder
		var targetPaths []string
		for _, vd := range volDefs {
			initMounts.WriteString(fmt.Sprintf("\n        - name: %s\n          mountPath: %s", vd.Name, vd.MountPath))
			targetPaths = append(targetPaths, vd.MountPath)
		}

		pathsArg := strings.Join(targetPaths, " ")
		initContainersSection = fmt.Sprintf(`
      initContainers:
      - name: init-volume-permissions
        image: busybox:1.36
        imagePullPolicy: IfNotPresent
        command: ["sh", "-c", "chmod -R 777 %[1]s 2>/dev/null || true; chown -R 1000:1000 %[1]s 2>/dev/null || true"]
        volumeMounts:%[2]s`, pathsArg, initMounts.String())
	}

	// Use TCP socket probes instead of HTTP — works for ALL apps
	// regardless of whether they have a /health endpoint.
	// startupProbe gives the app up to 150s (30 * 5s) to boot before
	// liveness/readiness probes kick in. Helm timeout (300s) is set
	// to exceed this window to avoid deadline race conditions.
	deploymentManifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    app: %[1]s
    managed-by: idlistack
spec:
  replicas: %[3]d
  revisionHistoryLimit: 3
  selector:
    matchLabels:
      app: %[1]s
  template:
    metadata:
      labels:
        app: %[1]s
    spec:
      securityContext:
        runAsNonRoot: false
        fsGroup: 1000
        fsGroupChangePolicy: Always%[14]s
      containers:
      - name: %[1]s
        image: %[4]s
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: %[5]d
        resources:
          requests:
            memory: "%[6]s"
            cpu: "%[7]s"
          limits:
            memory: "%[8]s"
            cpu: "%[9]s"
        startupProbe:
          tcpSocket:
            port: %[5]d
          initialDelaySeconds: 5
          periodSeconds: 5
          failureThreshold: 30
          timeoutSeconds: 3
        livenessProbe:
          tcpSocket:
            port: %[5]d
          initialDelaySeconds: 0
          periodSeconds: 15
          failureThreshold: 3
          timeoutSeconds: 3
        readinessProbe:
          tcpSocket:
            port: %[5]d
          initialDelaySeconds: 0
          periodSeconds: 5
          failureThreshold: 3
          timeoutSeconds: 3%[10]s%[11]s%[12]s%[13]s
`,
		d.appName,                   // 1
		d.namespace,                 // 2
		replicas,                    // 3
		d.imageTag,                  // 4
		port,                        // 5
		d.plan.Resources.Memory,     // 6
		d.plan.Resources.CPU,        // 7
		d.plan.Resources.Memory,     // 8
		d.plan.Resources.CPU,        // 9
		envFromSection,              // 10
		envSection,                  // 11
		volumeMountsSection,         // 12
		volumesSection,              // 13
		initContainersSection,       // 14
	)

	if err := d.writeManifest("deployment", deploymentManifest); err != nil {
		return fmt.Errorf("deployment template failed: %w", err)
	}

	// Write PVC manifests for persistent volumes
	if pvcManifests != "" {
		if err := d.writeManifest("persistent-volumes", pvcManifests); err != nil {
			return fmt.Errorf("persistent volume template failed: %w", err)
		}
	}

	return nil
}

// ─── Pre-deploy ─────────────────────────────────────────────────────────

func (d *Deployer) runPreDeploy(ctx context.Context) error {
	jobManifest := fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s-predeploy
  namespace: %s
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-delete-policy": before-hook-creation
spec:
  backoffLimit: 1
  ttlSecondsAfterFinished: 300
  template:
    spec:
      containers:
      - name: predeploy
        image: %s
        imagePullPolicy: IfNotPresent
        command: ["/bin/sh", "-c", "%s"]
      restartPolicy: Never
`, d.appName, d.namespace, d.imageTag, d.plan.PreDeployCmd)

	return d.writeManifest("job-predeploy", jobManifest)
}

// ─── Rollout ────────────────────────────────────────────────────────────

func (d *Deployer) helmCmd(ctx context.Context, args ...string) *exec.Cmd {
	if d.kubeToken != "" {
		args = append(args, "--kube-token="+d.kubeToken)
	}
	return exec.CommandContext(ctx, "helm", args...)
}

func (d *Deployer) executeHelmDeploy(ctx context.Context) error {
	helmDir := filepath.Join(d.projectDir, ".idlistack", "helm")

	// Helm timeout must exceed the startup probe window + buffer.
	// startupProbe: failureThreshold(30) * periodSeconds(5) = 150s
	// Buffer: 150s → total 300s. This prevents the race condition where
	// Helm's deadline fires before Kubernetes finishes probing.
	helmTimeout := "300s"

	cmd := d.helmCmd(ctx, "upgrade", "--install", d.appName, helmDir,
		"--namespace", d.namespace,
		"--create-namespace",
		"--wait", "--timeout", helmTimeout,
		"--atomic") // auto-rollback on failure; auto-purge on failed first install
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (d *Deployer) rollback(ctx context.Context) {
	// Check if there's a prior revision to roll back to.
	// On a first-install failure, there's no revision 0 — helm rollback
	// would fail with "release has no 0 version". Use uninstall instead.
	histCmd := d.helmCmd(ctx, "history", d.appName,
		"-n", d.namespace, "--max", "2", "-o", "json")
	output, _ := histCmd.Output()

	// Count deployed revisions (not pending/failed ones)
	hasDeployedRevision := false
	if len(output) > 2 {
		var history []struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(output, &history); err == nil {
			for _, h := range history {
				if h.Status == "deployed" || h.Status == "superseded" {
					hasDeployedRevision = true
					break
				}
			}
		}
	}

	if hasDeployedRevision {
		cmd := d.helmCmd(ctx, "rollback", d.appName, "-n", d.namespace)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	} else {
		// First install failed — no prior revision to roll back to.
		// Purge the failed release so the next deploy starts clean.
		ui.Warn("No previous successful revision — uninstalling failed release...")
		cmd := d.helmCmd(ctx, "uninstall", d.appName,
			"-n", d.namespace, "--no-hooks")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	}
}

func (d *Deployer) cleanupOldDeployment(ctx context.Context) {
	// Check if a previous failed/pending-install release exists.
	// Helm does NOT natively clean up zombie releases in pending-install
	// or pending-upgrade state — they block all future deploys.
	cmd := d.helmCmd(ctx, "status", d.appName,
		"-n", d.namespace, "-o", "json")
	output, err := cmd.Output()
	if err != nil {
		return // No existing release — clean slate
	}

	var status struct {
		Info struct {
			Status string `json:"status"`
		} `json:"info"`
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return
	}

	// Purge zombie releases that block helm upgrade --install
	s := status.Info.Status
	if s == "pending-install" || s == "pending-upgrade" || s == "failed" {
		ui.Warn(fmt.Sprintf("Cleaning up stuck release (status: %s)...", s))
		uninstallCmd := d.helmCmd(ctx, "uninstall", d.appName,
			"-n", d.namespace, "--no-hooks")
		uninstallCmd.Stdout = os.Stdout
		uninstallCmd.Stderr = os.Stderr
		uninstallCmd.Run()
		// Brief wait for Helm to finish cleanup
		time.Sleep(2 * time.Second)
	}
}

// showPodLogs fetches diagnostic logs, events, and pod statuses on deployment failure
func (d *Deployer) showPodLogs(ctx context.Context) {
	fmt.Println()
	ui.Info("─── Pod Statuses in Namespace ───")
	getPodsCmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", d.namespace, "-o", "wide")
	getPodsCmd.Stdout = os.Stdout
	getPodsCmd.Stderr = os.Stderr
	_ = getPodsCmd.Run()

	// Show any warning events in the namespace (ImagePullBackOff, CrashLoopBackOff, etc.)
	eventsCmd := exec.CommandContext(ctx, "kubectl", "get", "events", "-n", d.namespace,
		"--field-selector", "type=Warning", "--sort-by=.metadata.creationTimestamp")
	if eventsOut, err := eventsCmd.Output(); err == nil && len(strings.TrimSpace(string(eventsOut))) > 0 {
		fmt.Println()
		ui.Warn("─── K3s Warning Events ───")
		fmt.Println(string(eventsOut))
	}

	// Fetch logs for all pods in the namespace
	fmt.Println()
	ui.Info("─── Pod Logs (last 30 lines) ───")
	cmd := exec.CommandContext(ctx, "kubectl", "logs",
		"-n", d.namespace, "--selector", "managed-by=idlistack",
		"--tail=30", "--all-containers=true", "--prefix=true")
	output, err := cmd.Output()
	if err == nil && len(strings.TrimSpace(string(output))) > 0 {
		fmt.Println(string(output))
	} else {
		fallbackCmd := exec.CommandContext(ctx, "kubectl", "logs",
			"-n", d.namespace, "-l", fmt.Sprintf("app=%s", d.appName),
			"--tail=30", "--all-containers=true")
		if fbOut, fbErr := fallbackCmd.Output(); fbErr == nil && len(fbOut) > 0 {
			fmt.Println(string(fbOut))
		}
	}
}

// ─── Service URL & NodePort Resolution ───────────────────────────────────

func (d *Deployer) getServiceURL(ctx context.Context) string {
	// 1. Get NodePort
	cmd := exec.CommandContext(ctx, "kubectl", "get", "svc", d.appName, "-n", d.namespace, "-o", "jsonpath={.spec.ports[0].nodePort}")
	out, err := cmd.Output()
	var nodePort string
	if err == nil {
		nodePort = strings.TrimSpace(string(out))
	}
	if nodePort == "" {
		nodePort = fmt.Sprintf("%d", d.allocateNodePort(ctx))
	}

	// 2. Get K3s Node IP
	nodeIp := d.getNodeIP(ctx)

	return fmt.Sprintf("http://%s:%s", nodeIp, nodePort)
}

func (d *Deployer) getNodeIP(ctx context.Context) string {
	cmdIp := exec.CommandContext(ctx, "kubectl", "get", "nodes", "-o", `jsonpath={.items[0].status.addresses[?(@.type=="InternalIP")].address}`)
	outIp, err := cmdIp.Output()
	if err != nil {
		return "127.0.0.1"
	}
	nodeIp := strings.TrimSpace(string(outIp))
	if nodeIp == "" {
		return "127.0.0.1"
	}
	return strings.Fields(nodeIp)[0]
}

func (d *Deployer) allocateNodePort(ctx context.Context) int {
	// 1. Check if the service already exists in this namespace
	cmdExisting := exec.CommandContext(ctx, "kubectl", "get", "svc", d.appName, "-n", d.namespace, "-o", "jsonpath={.spec.ports[0].nodePort}")
	if out, err := cmdExisting.Output(); err == nil {
		var port int
		if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &port); err == nil && port >= 30000 && port <= 32767 {
			return port
		}
	}

	// 2. Query all currently used NodePorts across all namespaces
	cmdUsed := exec.CommandContext(ctx, "kubectl", "get", "svc", "-A", "-o", "jsonpath={.items[*].spec.ports[*].nodePort}")
	usedPorts := make(map[int]bool)
	if out, err := cmdUsed.Output(); err == nil {
		for _, field := range strings.Fields(string(out)) {
			var p int
			if _, err := fmt.Sscanf(field, "%d", &p); err == nil && p > 0 {
				usedPorts[p] = true
			}
		}
	}

	// 3. Deterministic starting offset based on appName hash to avoid port collisions
	h := fnv.New32a()
	h.Write([]byte(d.appName))
	offset := int(h.Sum32() % 2000)
	basePort := 30500 + offset

	for port := basePort; port <= 32767; port++ {
		if !usedPorts[port] {
			return port
		}
	}
	for port := 30000; port < basePort; port++ {
		if !usedPorts[port] {
			return port
		}
	}
	return 30000
}

// ─── Secrets ────────────────────────────────────────────────────────────

func (d *Deployer) checkSecretsExist(ctx context.Context, secretName string) bool {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "secret", secretName,
		"-n", d.namespace, "--ignore-not-found", "-o", "name")
	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) != ""
}

// ─── Deploy Lock ────────────────────────────────────────────────────────

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock already held")
	}

	// Write PID
	fmt.Fprintf(f, "%d\n%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	return f, nil
}

func releaseLock(f *os.File, path string) {
	if f != nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
	os.Remove(path)
}

// ─── Helpers ────────────────────────────────────────────────────────────

func (d *Deployer) writeManifest(name, manifest string) error {
	path := filepath.Join(d.projectDir, ".idlistack", "helm", "templates", name+".yaml")
	return os.WriteFile(path, []byte(manifest), 0644)
}

func (d *Deployer) extractDbCredentials() (user, pass, dbname string) {
	user = d.appName
	pass = d.appName
	dbname = d.appName

	// Try Frappe site_config.json if it exists
	if b, err := os.ReadFile(filepath.Join(d.projectDir, "sites", "currentsite.txt")); err == nil {
		siteName := strings.TrimSpace(string(b))
		if b, err := os.ReadFile(filepath.Join(d.projectDir, "sites", siteName, "site_config.json")); err == nil {
			type siteConfig struct {
				DbName     string `json:"db_name"`
				DbPassword string `json:"db_password"`
			}
			var sc siteConfig
			if err := json.Unmarshal(b, &sc); err == nil {
				if sc.DbName != "" {
					dbname = sc.DbName
					user = sc.DbName
				}
				if sc.DbPassword != "" {
					pass = sc.DbPassword
				}
			}
		}
	}

	if d.plan != nil && d.plan.Env != nil {
		for k, v := range d.plan.Env {
			if strings.Contains(v, "{{") {
				continue
			}
			lower := strings.ToLower(k)
			if strings.Contains(lower, "user") {
				user = v
			}
			if strings.Contains(lower, "password") || strings.Contains(lower, "pass") {
				pass = v
			}
			if strings.Contains(lower, "database") || strings.Contains(lower, "db") {
				if v != "mysql" && v != "postgres" && v != "db" && v != "localhost" && !strings.Contains(lower, "client") && !strings.Contains(lower, "host") {
					dbname = v
				}
			}
		}
	}

	files := []string{"config.toml", "config.example.toml", ".env", ".env.example", ".env.local"}
	for _, fname := range files {
		fpath := filepath.Join(d.projectDir, fname)
		content, err := os.ReadFile(fpath)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
				continue
			}
			lower := strings.ToLower(line)
			if strings.Contains(lower, "postgres_user") || (strings.HasPrefix(lower, "user") && strings.Contains(line, "=")) {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					val := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
					if val != "" {
						user = val
					}
				}
			}
			if strings.Contains(lower, "postgres_password") || (strings.HasPrefix(lower, "password") && strings.Contains(line, "=")) {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					val := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
					if val != "" {
						pass = val
					}
				}
			}
			if strings.Contains(lower, "postgres_db") || (strings.HasPrefix(lower, "name") && strings.Contains(line, "=")) || (strings.HasPrefix(lower, "database") && strings.Contains(line, "=")) {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					val := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
					if val != "" && val != "postgres" && val != "db" && val != "localhost" {
						dbname = val
					}
				}
			}
		}
	}
	return user, pass, dbname
}

// detectDependencies identifies backing services (PostgreSQL, MySQL/MariaDB, Redis)
func (d *Deployer) detectDependencies() (needsPostgres, needsMysql, needsRedis bool, mysqlImage, mysqlCommand string) {
	if d.projectDir == "" {
		return
	}

	allDeps := make([]string, 0)
	if len(d.config.Deploy.Dependencies) > 0 {
		allDeps = append(allDeps, d.config.Deploy.Dependencies...)
	}
	if d.plan != nil && len(d.plan.Dependencies) > 0 {
		allDeps = append(allDeps, d.plan.Dependencies...)
	}

	if len(allDeps) > 0 {
		for _, dep := range allDeps {
			parts := strings.Split(dep, " ")
			base := strings.ToLower(parts[0])
			if strings.HasPrefix(base, "postgres") || base == "db" {
				needsPostgres = true
			}
			if strings.HasPrefix(base, "redis") {
				needsRedis = true
			}
			if strings.HasPrefix(base, "mysql") || strings.HasPrefix(base, "mariadb") {
				needsMysql = true
				mysqlImage = parts[0]
				if len(parts) > 1 {
					var cmdArgs []string
					for _, arg := range parts[1:] {
						cmdArgs = append(cmdArgs, fmt.Sprintf("        - %s", arg))
					}
					mysqlCommand = fmt.Sprintf("args:\n        - mysqld\n%s", strings.Join(cmdArgs, "\n"))
				}
			}
		}
	} else {
		sqlFiles, _ := filepath.Glob(filepath.Join(d.projectDir, "*.sql"))
		if len(sqlFiles) > 0 {
			needsMysql = true
		}
		filesToScan := []string{"docker-compose.yml", "docker-compose.yaml", "config.toml", "config.example.toml", ".env", ".env.example", "idlistack.toml", "config/config.php", "config.php", "wp-config.php"}
		for _, fname := range filesToScan {
			fpath := filepath.Join(d.projectDir, fname)
			content, err := os.ReadFile(fpath)
			if err != nil {
				continue
			}
			str := strings.ToLower(string(content))
			if strings.Contains(str, "postgres") || strings.Contains(str, "host = \"db\"") || strings.Contains(str, "host=\"db\"") || strings.Contains(str, "db:5432") || strings.Contains(str, "5432") {
				needsPostgres = true
			}
			if strings.Contains(str, "redis") || strings.Contains(str, "host = \"redis\"") || strings.Contains(str, "host=\"redis\"") || strings.Contains(str, "redis:6379") || strings.Contains(str, "6379") {
				needsRedis = true
			}
			if strings.Contains(str, "mysql") || strings.Contains(str, "mariadb") || strings.Contains(str, "mysqli") || strings.Contains(str, "pdo_mysql") || strings.Contains(str, "3306") {
				needsMysql = true
			}
		}
	}

	if needsMysql {
		if mysqlImage == "" || mysqlImage == "mysql" {
			mysqlImage = "mariadb:10.6"
		} else if mysqlImage == "mariadb" {
			mysqlImage = "mariadb:10.6"
		}
	}
	return
}

// GetRequiredImages returns all container images required by this application and its dependencies
func (d *Deployer) GetRequiredImages() []string {
	images := []string{d.imageTag}

	needsPostgres, needsMysql, needsRedis, mysqlImage, _ := d.detectDependencies()
	if needsPostgres {
		images = append(images, "postgres:15-alpine")
	}
	if needsMysql && mysqlImage != "" {
		images = append(images, mysqlImage)
	}
	if needsRedis {
		images = append(images, "redis:alpine")
	}

	// Check if busybox init container is needed for volume permissions
	if d.plan != nil && (len(d.plan.ComposeVolumes) > 0 || len(d.plan.Volumes) > 0) {
		images = append(images, "busybox:1.36")
	}

	seen := make(map[string]bool)
	var unique []string
	for _, img := range images {
		if img != "" && !seen[img] {
			seen[img] = true
			unique = append(unique, img)
		}
	}
	return unique
}

func (d *Deployer) ensureDependencies(ctx context.Context) error {
	if d.projectDir == "" {
		return nil
	}

	dbUser, dbPass, dbName := d.extractDbCredentials()
	needsPostgres, needsMysql, needsRedis, mysqlImage, mysqlCommand := d.detectDependencies()

	if needsPostgres {
		ui.Detail("Provisioning dependency: %s (db: %s, user: %s)", color.CyanString("Postgres (db)"), dbName, dbUser)
		postgresManifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: db
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: db
  template:
    metadata:
      labels:
        app: db
    spec:
      containers:
      - name: postgres
        image: postgres:15-alpine
        imagePullPolicy: IfNotPresent
        env:
        - name: POSTGRES_USER
          value: "%[2]s"
        - name: POSTGRES_PASSWORD
          value: "%[3]s"
        - name: POSTGRES_DB
          value: "%[4]s"
        - name: POSTGRES_HOST_AUTH_METHOD
          value: "trust"
        ports:
        - containerPort: 5432
        readinessProbe:
          exec:
            command: ["pg_isready", "-U", "%[2]s"]
          initialDelaySeconds: 2
          periodSeconds: 2
        volumeMounts:
        - name: db-data
          mountPath: /var/lib/postgresql/data
      volumes:
      - name: db-data
        persistentVolumeClaim:
          claimName: db-pvc
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: db-pvc
  namespace: %[1]s
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: Service
metadata:
  name: db
  namespace: %[1]s
spec:
  selector:
    app: db
  ports:
  - port: 5432
    targetPort: 5432
---
apiVersion: v1
kind: Service
metadata:
  name: postgres-db
  namespace: %[1]s
spec:
  selector:
    app: db
  ports:
  - port: 5432
    targetPort: 5432
`, d.namespace, dbUser, dbPass, dbName)
		_ = d.writeManifest("dependency-postgres", postgresManifest)
	}

	if needsMysql {
		ui.Detail("Provisioning dependency: %s (db: %s, user: %s)", color.CyanString("MariaDB/MySQL (db)"), dbName, dbUser)
		if mysqlImage == "" || mysqlImage == "mysql" {
			mysqlImage = "mariadb:10.6" // MariaDB is fast, stable, and uses lower memory
		} else if mysqlImage == "mariadb" {
			mysqlImage = "mariadb:10.6"
		}

		var commandInjection string
		if mysqlCommand != "" {
			commandInjection = fmt.Sprintf("        %s", strings.TrimSpace(mysqlCommand))
		}

		// Check for database initialization SQL file (e.g. database.sql)
		var sqlInitMount string
		var sqlInitVolume string
		var sqlConfigMap string

		sqlCandidate := filepath.Join(d.projectDir, "database.sql")
		if _, err := os.Stat(sqlCandidate); err != nil {
			sqls, _ := filepath.Glob(filepath.Join(d.projectDir, "*.sql"))
			if len(sqls) > 0 {
				sqlCandidate = sqls[0]
			} else {
				sqlCandidate = ""
			}
		}

		if sqlCandidate != "" {
			if sqlBytes, err := os.ReadFile(sqlCandidate); err == nil && len(sqlBytes) > 0 {
				var indented strings.Builder
				for _, line := range strings.Split(string(sqlBytes), "\n") {
					indented.WriteString("    " + line + "\n")
				}
				sqlConfigMap = fmt.Sprintf(`---
apiVersion: v1
kind: ConfigMap
metadata:
  name: db-init-sql
  namespace: %s
data:
  init.sql: |
%s`, d.namespace, indented.String())

				sqlInitMount = `
        - name: db-init
          mountPath: /docker-entrypoint-initdb.d/init.sql
          subPath: init.sql`
				sqlInitVolume = `
      - name: db-init
        configMap:
          name: db-init-sql`
			}
		}

		mysqlManifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: db
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: db
  template:
    metadata:
      labels:
        app: db
    spec:
      containers:
      - name: mysql
        image: %[5]s
%[6]s
        imagePullPolicy: IfNotPresent
        env:
        - name: MYSQL_USER
          value: "%[2]s"
        - name: MYSQL_PASSWORD
          value: "%[3]s"
        - name: MYSQL_DATABASE
          value: "%[4]s"
        - name: MYSQL_ROOT_PASSWORD
          value: "%[3]s"
        ports:
        - containerPort: 3306
        startupProbe:
          exec:
            command: ["mysqladmin", "ping", "-h", "127.0.0.1", "-u", "root", "-p%[3]s"]
          initialDelaySeconds: 5
          periodSeconds: 5
          failureThreshold: 30
          timeoutSeconds: 3
        readinessProbe:
          exec:
            command: ["mysqladmin", "ping", "-h", "127.0.0.1", "-u", "root", "-p%[3]s"]
          initialDelaySeconds: 5
          periodSeconds: 5
          timeoutSeconds: 3
        livenessProbe:
          exec:
            command: ["mysqladmin", "ping", "-h", "127.0.0.1", "-u", "root", "-p%[3]s"]
          initialDelaySeconds: 30
          periodSeconds: 15
          timeoutSeconds: 3
        volumeMounts:
        - name: db-data
          mountPath: /var/lib/mysql%[7]s
      volumes:
      - name: db-data
        persistentVolumeClaim:
          claimName: db-pvc%[8]s
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: db-pvc
  namespace: %[1]s
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: Service
metadata:
  name: db
  namespace: %[1]s
spec:
  selector:
    app: db
  ports:
  - port: 3306
    targetPort: 3306
---
apiVersion: v1
kind: Service
metadata:
  name: mysql-db
  namespace: %[1]s
spec:
  selector:
    app: db
  ports:
  - port: 3306
    targetPort: 3306
%[9]s
`, d.namespace, dbUser, dbPass, dbName, mysqlImage, commandInjection, sqlInitMount, sqlInitVolume, sqlConfigMap)
		_ = d.writeManifest("dependency-mysql", mysqlManifest)
	}

	if needsRedis {
		ui.Detail("Provisioning dependency: %s", color.CyanString("Redis (redis)"))
		redisManifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
    spec:
      containers:
      - name: redis
        image: redis:7-alpine
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 6379
        readinessProbe:
          tcpSocket:
            port: 6379
          initialDelaySeconds: 1
          periodSeconds: 2
        volumeMounts:
        - name: redis-data
          mountPath: /data
      volumes:
      - name: redis-data
        persistentVolumeClaim:
          claimName: redis-pvc
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: redis-pvc
  namespace: %[1]s
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Service
metadata:
  name: redis
  namespace: %[1]s
spec:
  selector:
    app: redis
  ports:
  - port: 6379
    targetPort: 6379
`, d.namespace)
		_ = d.writeManifest("dependency-redis", redisManifest)
	}
	return nil
}

func (d *Deployer) syncLocalDockerData(ctx context.Context) {
	// Dynamically check if a local container exists matching this app's database
	filterNames := []string{
		fmt.Sprintf("name=%s_postgres", d.appName),
		fmt.Sprintf("name=%s-postgres", d.appName),
		fmt.Sprintf("name=%s_db", d.appName),
		fmt.Sprintf("name=%s-db", d.appName),
	}
	var localContainer string
	for _, f := range filterNames {
		cmd := exec.CommandContext(ctx, "docker", "ps", "-q", "-f", f)
		if out, err := cmd.Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
			localContainer = strings.TrimSpace(string(out))
			break
		}
	}
	if localContainer == "" {
		return // No local container to sync from
	}
	
	// Create a marker so we only do this once
	markerFile := filepath.Join(d.projectDir, ".idlistack", "data_synced.lock")
	if _, err := os.Stat(markerFile); err == nil {
		return // Already synced
	}

	ui.Detail("Detected existing local database container. Dynamically migrating current data to K3s...")
	
	dbUser, _, dbName := d.extractDbCredentials()
	if dbUser == "" {
		dbUser = "postgres"
	}
	if dbName == "" {
		dbName = "postgres"
	}

	// Scale down the app to release database connections before we drop the database
	exec.CommandContext(ctx, "kubectl", "scale", "deployment", d.appName, "--replicas=0", "-n", d.namespace).Run()
	time.Sleep(3 * time.Second)
	
	dumpFile := fmt.Sprintf("/tmp/%s_dump_auto.sql", d.appName)
	// Dump
	dumpCmd := exec.CommandContext(ctx, "bash", "-c", fmt.Sprintf("docker exec %s pg_dumpall -c -U %s > %s", localContainer, dbUser, dumpFile))
	if err := dumpCmd.Run(); err != nil {
		ui.Warn("Failed to dump local data: " + err.Error())
		exec.CommandContext(ctx, "kubectl", "scale", "deployment", d.appName, "--replicas=1", "-n", d.namespace).Run()
		return
	}
	
	// Restore
	restoreCmd := exec.CommandContext(ctx, "bash", "-c", fmt.Sprintf("kubectl exec -i -n %s deployment/db -- psql -U %s -d %s < %s", d.namespace, dbUser, dbName, dumpFile))
	if err := restoreCmd.Run(); err != nil {
		ui.Warn("Failed to restore data to K3s: " + err.Error())
		exec.CommandContext(ctx, "kubectl", "scale", "deployment", d.appName, "--replicas=1", "-n", d.namespace).Run()
		return
	}
	
	// Scale up the app again
	exec.CommandContext(ctx, "kubectl", "scale", "deployment", d.appName, "--replicas=1", "-n", d.namespace).Run()
	
	os.WriteFile(markerFile, []byte("done"), 0644)
	ui.Detail("Current data successfully migrated to dynamic environment!")
}
