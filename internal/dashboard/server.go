package dashboard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/idlistack/cli/internal/buildplan"
	"github.com/idlistack/cli/internal/config"
)

// Server represents the IdliStack Dashboard web server
type Server struct {
	config     *config.Config
	projectDir string
	appName    string
	namespace  string
	port       int
	listener   net.Listener
	httpServer *http.Server
}

// PodInfo represents summarized pod information
type PodInfo struct {
	Name       string `json:"name"`
	Phase      string `json:"phase"`
	StatusText string `json:"statusText"`
	Ready      bool   `json:"ready"`
	ReadyCount string `json:"readyCount"`
	Restarts   int    `json:"restarts"`
	Age        string `json:"age"`
	PodIP      string `json:"podIp"`
	NodeName   string `json:"nodeName"`
}

// ServicePortInfo represents a port exposed by a service
type ServicePortInfo struct {
	Port       int    `json:"port"`
	TargetPort any    `json:"targetPort"`
	NodePort   int    `json:"nodePort,omitempty"`
	Protocol   string `json:"protocol"`
}

// ServiceInfo represents a Kubernetes service in the namespace
type ServiceInfo struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	ClusterIP string            `json:"clusterIp"`
	Ports     []ServicePortInfo `json:"ports"`
}

// PVCInfo represents persistent storage claims
type PVCInfo struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Capacity   string `json:"capacity"`
	AccessMode string `json:"accessMode"`
}

// DependencyInfo represents companion service information
type DependencyInfo struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Ready  bool   `json:"ready"`
	Image  string `json:"image"`
}

// DeploymentInfo represents deployment metadata and replica status
type DeploymentInfo struct {
	Name              string `json:"name"`
	SpecReplicas      int    `json:"specReplicas"`
	ReadyReplicas     int    `json:"readyReplicas"`
	AvailableReplicas int    `json:"availableReplicas"`
	Image             string `json:"image"`
	CreationTimestamp string `json:"creationTimestamp"`
}

// StatusResponse is the full JSON payload returned by /api/status
type StatusResponse struct {
	Project         map[string]any   `json:"project"`
	Namespace       string           `json:"namespace"`
	ProjectDir      string           `json:"projectDir"`
	Url             string           `json:"url"`
	NodeIp          string           `json:"nodeIp"`
	NodePort        string           `json:"nodePort"`
	Port            int              `json:"port"`
	ImageTag        string           `json:"imageTag"`
	HealthCheckPath string           `json:"healthCheckPath"`
	BuildPlan       *buildplan.Plan  `json:"buildPlan,omitempty"`
	Deployment      *DeploymentInfo  `json:"deployment,omitempty"`
	Pods            []PodInfo        `json:"pods"`
	Services        []ServiceInfo    `json:"services"`
	Dependencies    []DependencyInfo `json:"dependencies"`
	PVCs            []PVCInfo        `json:"pvcs"`
}

// NewServer initializes the dashboard web server on an available port
func NewServer(cfg *config.Config, projectDir string, preferredPort int) (*Server, error) {
	appName := strings.ToLower(cfg.Project.Name)
	namespace := fmt.Sprintf("idlistack-%s", appName)

	if preferredPort <= 0 {
		preferredPort = 4200
	}

	// Find an available port starting from preferredPort
	var listener net.Listener
	var actualPort int
	for p := preferredPort; p < preferredPort+50; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			listener = l
			actualPort = p
			break
		}
	}

	if listener == nil {
		return nil, fmt.Errorf("could not find an available port between %d and %d", preferredPort, preferredPort+50)
	}

	s := &Server{
		config:     cfg,
		projectDir: projectDir,
		appName:    appName,
		namespace:  namespace,
		port:       actualPort,
		listener:   listener,
	}

	mux := http.NewServeMux()

	// Static assets from embedded FS
	fsHandler, err := GetFileSystem()
	if err != nil {
		return nil, fmt.Errorf("failed to load dashboard static filesystem: %w", err)
	}
	fileServer := http.FileServer(fsHandler)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}
		fileServer.ServeHTTP(w, r)
	})

	// API Endpoints
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/health-check", s.handleHealthCheck)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/env", s.handleEnv)
	mux.HandleFunc("/api/exec", s.handleExec)
	mux.HandleFunc("/api/database/backup", s.handleDatabaseBackup)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/history/rollback", s.handleHistoryRollback)
	mux.HandleFunc("/api/actions/stop", s.handleActionStop)
	mux.HandleFunc("/api/actions/start", s.handleActionStart)
	mux.HandleFunc("/api/actions/restart", s.handleActionRestart)
	mux.HandleFunc("/api/actions/scale", s.handleActionScale)

	s.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	return s, nil
}

// Start runs the HTTP server in a background goroutine
func (s *Server) Start() error {
	go func() {
		_ = s.httpServer.Serve(s.listener)
	}()
	return nil
}

// Stop gracefully shuts down the HTTP server
func (s *Server) Stop(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// URL returns the local dashboard URL
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

// Port returns the server's bound port
func (s *Server) Port() int {
	return s.port
}

// OpenBrowser opens the user's default web browser to the dashboard URL
func (s *Server) OpenBrowser() error {
	url := s.URL()
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform for browser launch")
	}

	return cmd.Start()
}

// ─── API Handlers ───────────────────────────────────────────────────────────

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()

	resp := StatusResponse{
		Project: map[string]any{
			"name":     s.config.Project.Name,
			"provider": s.config.Build.Provider,
		},
		Namespace:       s.namespace,
		ProjectDir:      s.projectDir,
		Port:            s.config.Deploy.Port,
		HealthCheckPath: s.config.Deploy.HealthCheckPath,
		Pods:            []PodInfo{},
		Services:        []ServiceInfo{},
		Dependencies:    []DependencyInfo{},
		PVCs:            []PVCInfo{},
	}

	// 1. Try reading .idlistack.json (or fallback to legacy buildplan.json)
	if state, err := config.LoadProjectState(s.projectDir); err == nil && state.Plan != nil {
		resp.BuildPlan = state.Plan
		if resp.Port == 0 {
			resp.Port = state.Plan.Port
		}
	} else {
		buildPlanPath := filepath.Join(s.projectDir, ".idlistack", "buildplan.json")
		if data, err := os.ReadFile(buildPlanPath); err == nil {
			var plan buildplan.Plan
			if err := json.Unmarshal(data, &plan); err == nil {
				resp.BuildPlan = &plan
				if resp.Port == 0 {
					resp.Port = plan.Port
				}
			}
		}
	}

	// 2. Query Deployment Info
	deployCmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", s.appName, "-n", s.namespace, "-o", "json")
	if out, err := deployCmd.Output(); err == nil {
		var dData map[string]any
		if err := json.Unmarshal(out, &dData); err == nil {
			depInfo := &DeploymentInfo{
				Name: s.appName,
			}
			if spec, ok := dData["spec"].(map[string]any); ok {
				if r, ok := spec["replicas"].(float64); ok {
					depInfo.SpecReplicas = int(r)
				}
				if tmpl, ok := spec["template"].(map[string]any); ok {
					if tSpec, ok := tmpl["spec"].(map[string]any); ok {
						if containers, ok := tSpec["containers"].([]any); ok && len(containers) > 0 {
							if c0, ok := containers[0].(map[string]any); ok {
								if img, ok := c0["image"].(string); ok {
									depInfo.Image = img
									resp.ImageTag = img
								}
							}
						}
					}
				}
			}
			if st, ok := dData["status"].(map[string]any); ok {
				if r, ok := st["readyReplicas"].(float64); ok {
					depInfo.ReadyReplicas = int(r)
				}
				if a, ok := st["availableReplicas"].(float64); ok {
					depInfo.AvailableReplicas = int(a)
				}
			}
			if meta, ok := dData["metadata"].(map[string]any); ok {
				if ts, ok := meta["creationTimestamp"].(string); ok {
					depInfo.CreationTimestamp = ts
				}
			}
			resp.Deployment = depInfo
		}
	}

	// 3. Query Pods in namespace
	podsCmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", s.namespace, "-o", "json")
	if out, err := podsCmd.Output(); err == nil {
		var pList struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(out, &pList); err == nil {
			for _, item := range pList.Items {
				metadata, _ := item["metadata"].(map[string]any)
				spec, _ := item["spec"].(map[string]any)
				status, _ := item["status"].(map[string]any)

				podName, _ := metadata["name"].(string)
				phase, _ := status["phase"].(string)
				nodeName, _ := spec["nodeName"].(string)
				podIP, _ := status["podIP"].(string)

				statusText := phase
				readyContainers := 0
				totalContainers := 0
				restarts := 0

				if cStatuses, ok := status["containerStatuses"].([]any); ok {
					totalContainers = len(cStatuses)
					for _, csAny := range cStatuses {
						cs, ok := csAny.(map[string]any)
						if !ok {
							continue
						}
						if ready, ok := cs["ready"].(bool); ok && ready {
							readyContainers++
						}
						if rc, ok := cs["restartCount"].(float64); ok {
							restarts += int(rc)
						}
						if state, ok := cs["state"].(map[string]any); ok {
							if waiting, ok := state["waiting"].(map[string]any); ok {
								if reason, ok := waiting["reason"].(string); ok && reason != "" {
									statusText = reason
								}
							} else if terminated, ok := state["terminated"].(map[string]any); ok {
								if reason, ok := terminated["reason"].(string); ok && reason != "" {
									statusText = reason
								}
							}
						}
					}
				}

				// Calculate Human Age
				age := "Unknown"
				if tsStr, ok := metadata["creationTimestamp"].(string); ok {
					if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
						age = formatDuration(time.Since(t))
					}
				}

				resp.Pods = append(resp.Pods, PodInfo{
					Name:       podName,
					Phase:      phase,
					StatusText: statusText,
					Ready:      readyContainers == totalContainers && totalContainers > 0,
					ReadyCount: fmt.Sprintf("%d/%d", readyContainers, totalContainers),
					Restarts:   restarts,
					Age:        age,
					PodIP:      podIP,
					NodeName:   nodeName,
				})

				// Check if it's a companion dependency pod (db or redis)
				if strings.HasPrefix(podName, "db-") {
					resp.Dependencies = append(resp.Dependencies, DependencyInfo{
						Name:   "Database (db)",
						Type:   "Relational Database",
						Status: statusText,
						Ready:  readyContainers == totalContainers && totalContainers > 0,
						Image:  "MariaDB / PostgreSQL",
					})
				} else if strings.HasPrefix(podName, "redis-") {
					resp.Dependencies = append(resp.Dependencies, DependencyInfo{
						Name:   "Redis (redis)",
						Type:   "In-Memory Cache",
						Status: statusText,
						Ready:  readyContainers == totalContainers && totalContainers > 0,
						Image:  "redis:7-alpine",
					})
				}
			}
		}
	}

	// 4. Query Services
	svcCmd := exec.CommandContext(ctx, "kubectl", "get", "svc", "-n", s.namespace, "-o", "json")
	if out, err := svcCmd.Output(); err == nil {
		var sList struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(out, &sList); err == nil {
			for _, item := range sList.Items {
				metadata, _ := item["metadata"].(map[string]any)
				spec, _ := item["spec"].(map[string]any)

				sName, _ := metadata["name"].(string)
				sType, _ := spec["type"].(string)
				cIP, _ := spec["clusterIP"].(string)

				var ports []ServicePortInfo
				if pArr, ok := spec["ports"].([]any); ok {
					for _, pAny := range pArr {
						pMap, ok := pAny.(map[string]any)
						if !ok {
							continue
						}
						portNum := 0
						if p, ok := pMap["port"].(float64); ok {
							portNum = int(p)
						}
						targetP := pMap["targetPort"]
						nodeP := 0
						if np, ok := pMap["nodePort"].(float64); ok {
							nodeP = int(np)
						}
						proto, _ := pMap["protocol"].(string)
						if proto == "" {
							proto = "TCP"
						}
						ports = append(ports, ServicePortInfo{
							Port:       portNum,
							TargetPort: targetP,
							NodePort:   nodeP,
							Protocol:   proto,
						})

						if sName == s.appName && nodeP > 0 {
							resp.NodePort = fmt.Sprintf("%d", nodeP)
						}
					}
				}

				resp.Services = append(resp.Services, ServiceInfo{
					Name:      sName,
					Type:      sType,
					ClusterIP: cIP,
					Ports:     ports,
				})
			}
		}
	}

	// 5. Query PersistentVolumeClaims
	pvcCmd := exec.CommandContext(ctx, "kubectl", "get", "pvc", "-n", s.namespace, "-o", "json")
	if out, err := pvcCmd.Output(); err == nil {
		var pvcList struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(out, &pvcList); err == nil {
			for _, item := range pvcList.Items {
				metadata, _ := item["metadata"].(map[string]any)
				status, _ := item["status"].(map[string]any)
				spec, _ := item["spec"].(map[string]any)

				pvcName, _ := metadata["name"].(string)
				pvcStatus, _ := status["phase"].(string)
				capStr := "5Gi"
				if capMap, ok := status["capacity"].(map[string]any); ok {
					if stg, ok := capMap["storage"].(string); ok {
						capStr = stg
					}
				}
				modeStr := "RWO"
				if modes, ok := spec["accessModes"].([]any); ok && len(modes) > 0 {
					if m, ok := modes[0].(string); ok {
						modeStr = m
					}
				}

				resp.PVCs = append(resp.PVCs, PVCInfo{
					Name:       pvcName,
					Status:     pvcStatus,
					Capacity:   capStr,
					AccessMode: modeStr,
				})
			}
		}
	}

	// 6. Resolve Node IP and Service URL
	nodeIPCmd := exec.CommandContext(ctx, "kubectl", "get", "nodes", "-o", `jsonpath={.items[0].status.addresses[?(@.type=="InternalIP")].address}`)
	nodeIP := "127.0.0.1"
	if out, err := nodeIPCmd.Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		nodeIP = strings.Fields(strings.TrimSpace(string(out)))[0]
	}
	resp.NodeIp = nodeIP

	if resp.NodePort != "" {
		resp.Url = fmt.Sprintf("http://%s:%s", nodeIP, resp.NodePort)
	}

	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	targetURL := r.URL.Query().Get("url")
	if targetURL == "" {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "missing url"})
		return
	}

	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Do not follow redirects that try to switch to https or cross schemes
			return http.ErrUseLastResponse
		},
	}

	start := time.Now()
	resp, err := client.Get(targetURL)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{
			"ok":        false,
			"error":     err.Error(),
			"latencyMs": latency,
		})
		return
	}
	defer resp.Body.Close()

	// Both 2xx and 3xx are considered reachable/active responses
	json.NewEncoder(w).Encode(map[string]any{
		"ok":         resp.StatusCode < 400 || resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently,
		"statusCode": resp.StatusCode,
		"latencyMs":  latency,
	})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	podName := r.URL.Query().Get("pod")
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "100"
	}

	if podName == "" {
		// Default to app pod
		podName = s.appName
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "logs", podName, "-n", s.namespace, fmt.Sprintf("--tail=%s", tail))
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("Error reading container logs: %v\n%s", err, string(out))))
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(out)
}

func (s *Server) handleActionStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "scale", "deployment", s.appName, "--replicas=0", "-n", s.namespace)
	out, err := cmd.CombinedOutput()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("%v: %s", err, string(out)),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleActionStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	replicas := s.config.Deploy.Replicas
	if replicas <= 0 {
		replicas = 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "scale", "deployment", s.appName, fmt.Sprintf("--replicas=%d", replicas), "-n", s.namespace)
	out, err := cmd.CombinedOutput()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("%v: %s", err, string(out)),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleActionRestart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "rollout", "restart", fmt.Sprintf("deployment/%s", s.appName), "-n", s.namespace)
	out, err := cmd.CombinedOutput()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("%v: %s", err, string(out)),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleActionScale(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Replicas int `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid json"})
		return
	}

	if req.Replicas < 0 || req.Replicas > 10 {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "replicas must be between 0 and 10"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "scale", "deployment", s.appName, fmt.Sprintf("--replicas=%d", req.Replicas), "-n", s.namespace)
	out, err := cmd.CombinedOutput()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("%v: %s", err, string(out)),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"ok": true, "replicas": req.Replicas})
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "top", "pod", "-n", s.namespace, "--no-headers")
	out, err := cmd.Output()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "Metrics server not responding or pods starting", "pods": []any{}})
		return
	}

	type PodMetric struct {
		Name          string `json:"name"`
		Cpu           string `json:"cpu"`
		Memory        string `json:"memory"`
		CpuMillicores int    `json:"cpuMillicores"`
		MemoryBytes   int64  `json:"memoryBytes"`
	}

	var podMetrics []PodMetric
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			cpuStr := fields[1]
			memStr := fields[2]
			podMetrics = append(podMetrics, PodMetric{
				Name:          fields[0],
				Cpu:           cpuStr,
				Memory:        memStr,
				CpuMillicores: parseMillicores(cpuStr),
				MemoryBytes:   parseMemoryBytes(memStr),
			})
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"pods": podMetrics,
	})
}

func parseMillicores(s string) int {
	s = strings.TrimSuffix(s, "m")
	val, _ := strconv.Atoi(s)
	return val
}

func parseMemoryBytes(s string) int64 {
	if strings.HasSuffix(s, "Mi") {
		val, _ := strconv.ParseInt(strings.TrimSuffix(s, "Mi"), 10, 64)
		return val * 1024 * 1024
	}
	if strings.HasSuffix(s, "Gi") {
		val, _ := strconv.ParseInt(strings.TrimSuffix(s, "Gi"), 10, 64)
		return val * 1024 * 1024 * 1024
	}
	if strings.HasSuffix(s, "Ki") {
		val, _ := strconv.ParseInt(strings.TrimSuffix(s, "Ki"), 10, 64)
		return val * 1024
	}
	val, _ := strconv.ParseInt(s, 10, 64)
	return val
}

func (s *Server) handleEnv(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	secretName := fmt.Sprintf("%s-env", s.appName)

	if r.Method == http.MethodGet {
		envMap := make(map[string]string)
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "kubectl", "get", "secret", secretName, "-n", s.namespace, "-o", "json")
		if out, err := cmd.Output(); err == nil {
			var sec struct {
				Data map[string]string `json:"data"`
			}
			if err := json.Unmarshal(out, &sec); err == nil {
				for k, vBase64 := range sec.Data {
					if decoded, err := base64.StdEncoding.DecodeString(vBase64); err == nil {
						envMap[k] = string(decoded)
					}
				}
			}
		}

		if len(envMap) == 0 {
			if s.config.Env != nil {
				for k, v := range s.config.Env {
					envMap[k] = v
				}
			}
			if state, err := config.LoadProjectState(s.projectDir); err == nil && state.Plan != nil && state.Plan.Env != nil {
				for k, v := range state.Plan.Env {
					if _, exists := envMap[k]; !exists {
						envMap[k] = v
					}
				}
			} else {
				buildPlanPath := filepath.Join(s.projectDir, ".idlistack", "buildplan.json")
				if data, err := os.ReadFile(buildPlanPath); err == nil {
					var plan buildplan.Plan
					if err := json.Unmarshal(data, &plan); err == nil && plan.Env != nil {
						for k, v := range plan.Env {
							if _, exists := envMap[k]; !exists {
								envMap[k] = v
							}
						}
					}
				}
			}
		}

		json.NewEncoder(w).Encode(map[string]any{"ok": true, "env": envMap})
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			Env map[string]string `json:"env"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid json"})
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		args := []string{"create", "secret", "generic", secretName, "-n", s.namespace, "--dry-run=client", "-o", "yaml"}
		for k, v := range req.Env {
			k = strings.TrimSpace(k)
			if k != "" {
				args = append(args, fmt.Sprintf("--from-literal=%s=%s", k, v))
			}
		}

		createCmd := exec.CommandContext(ctx, "kubectl", args...)
		yamlOut, err := createCmd.Output()
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": fmt.Sprintf("Failed to generate secret: %v", err)})
			return
		}

		applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-n", s.namespace, "-f", "-")
		applyCmd.Stdin = bytes.NewReader(yamlOut)
		if applyOut, err := applyCmd.CombinedOutput(); err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": fmt.Sprintf("Failed to apply secret: %v (%s)", err, string(applyOut))})
			return
		}

		restartCmd := exec.CommandContext(ctx, "kubectl", "rollout", "restart", fmt.Sprintf("deployment/%s", s.appName), "-n", s.namespace)
		_ = restartCmd.Run()

		json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": "Environment updated and rolling restart triggered"})
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Command string `json:"command"`
		Pod     string `json:"pod"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid json"})
		return
	}

	if strings.TrimSpace(req.Command) == "" {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "command cannot be empty"})
		return
	}

	podName := req.Pod
	if podName == "" {
		ctxList, cancelList := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelList()
		cmdList := exec.CommandContext(ctxList, "kubectl", "get", "pods", "-n", s.namespace, "-l", fmt.Sprintf("app=%s", s.appName), "-o", "jsonpath={.items[0].metadata.name}")
		if out, err := cmdList.Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
			podName = strings.TrimSpace(string(out))
		} else {
			podName = fmt.Sprintf("deployment/%s", s.appName)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	execCmd := exec.CommandContext(ctx, "kubectl", "exec", podName, "-n", s.namespace, "--", "sh", "-c", req.Command)
	out, err := execCmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"output":   string(out),
		"exitCode": exitCode,
		"pod":      podName,
	})
}

func (s *Server) handleDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	checkCmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", s.namespace, "-o", "json")
	out, err := checkCmd.Output()
	if err != nil {
		http.Error(w, "Failed to query cluster pods", http.StatusInternalServerError)
		return
	}

	var pList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"items"`
	}
	_ = json.Unmarshal(out, &pList)

	dbPod := ""
	isPostgres := false
	isMysql := false

	for _, item := range pList.Items {
		pName := item.Metadata.Name
		if strings.HasPrefix(pName, "db-") || strings.HasPrefix(pName, "postgres-") || strings.HasPrefix(pName, "mysql-") {
			dbPod = pName
			for _, c := range item.Spec.Containers {
				if strings.Contains(c.Image, "postgres") {
					isPostgres = true
				} else if strings.Contains(c.Image, "mariadb") || strings.Contains(c.Image, "mysql") {
					isMysql = true
				}
			}
			break
		}
	}

	if dbPod == "" {
		http.Error(w, "No active database pod detected in this namespace", http.StatusNotFound)
		return
	}

	var dumpCmd *exec.Cmd
	if isPostgres {
		dumpCmd = exec.CommandContext(ctx, "kubectl", "exec", dbPod, "-n", s.namespace, "--", "pg_dumpall", "-U", "postgres")
	} else if isMysql {
		dumpCmd = exec.CommandContext(ctx, "kubectl", "exec", dbPod, "-n", s.namespace, "--", "sh", "-c", fmt.Sprintf("mysqldump -u root -p%s --all-databases 2>/dev/null || mysqldump -u %s -p%s --all-databases 2>/dev/null", s.appName, s.appName, s.appName))
	} else {
		dumpCmd = exec.CommandContext(ctx, "kubectl", "exec", dbPod, "-n", s.namespace, "--", "mysqldump", "--all-databases")
	}

	w.Header().Set("Content-Type", "application/sql")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-db-backup-%d.sql\"", s.appName, time.Now().Unix()))

	dumpCmd.Stdout = w
	dumpCmd.Stderr = os.Stderr
	_ = dumpCmd.Run()
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "helm", "history", s.appName, "-n", s.namespace, "-o", "json")
	out, err := cmd.Output()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "history": []any{}})
		return
	}

	var history []map[string]any
	if err := json.Unmarshal(out, &history); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "history": []any{}})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"ok": true, "history": history})
}

func (s *Server) handleHistoryRollback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Revision int `json:"revision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Revision <= 0 {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid revision"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "helm", "rollback", s.appName, fmt.Sprintf("%d", req.Revision), "-n", s.namespace)
	out, err := cmd.CombinedOutput()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("%v: %s", err, string(out)),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": fmt.Sprintf("Rolled back to revision %d", req.Revision)})
}

