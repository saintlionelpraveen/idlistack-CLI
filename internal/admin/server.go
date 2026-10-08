package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/idlistack/cli/internal/auth"
	"github.com/idlistack/cli/internal/k8s"
)

// Server represents the IdliStack Admin Dashboard server
type Server struct {
	port       int
	listener   net.Listener
	httpServer *http.Server
	nodeHost   string
}

// ClusterNode represents node details and health
type ClusterNode struct {
	Name             string            `json:"name"`
	Status           string            `json:"status"`
	Roles            string            `json:"roles"`
	Version          string            `json:"version"`
	InternalIP       string            `json:"internalIp"`
	ExternalIP       string            `json:"externalIp"`
	OSImage          string            `json:"osImage"`
	KernelVersion    string            `json:"kernelVersion"`
	ContainerRuntime string            `json:"containerRuntime"`
	CPUCapacity      string            `json:"cpuCapacity"`
	MemCapacity      string            `json:"memCapacity"`
	PodsCapacity     string            `json:"podsCapacity"`
	Conditions       map[string]string `json:"conditions"`
}

// AppPod represents a single pod in an application
type AppPod struct {
	Name       string `json:"name"`
	Phase      string `json:"phase"`
	Ready      bool   `json:"ready"`
	Restarts   int    `json:"restarts"`
	Age        string `json:"age"`
	PodIP      string `json:"podIp"`
	NodeName   string `json:"nodeName"`
	StatusText string `json:"statusText"`
}

// AppInfo represents a deployed application in a tenant namespace
type AppInfo struct {
	Name              string   `json:"name"`
	Namespace         string   `json:"namespace"`
	Owner             string   `json:"owner"`
	ReplicasDesired   int      `json:"replicasDesired"`
	ReplicasReady     int      `json:"replicasReady"`
	ReplicasAvailable int      `json:"replicasAvailable"`
	Image             string   `json:"image"`
	URL               string   `json:"url"`
	NodePort          int      `json:"nodePort"`
	Created           string   `json:"created"`
	Age               string   `json:"age"`
	Status            string   `json:"status"` // "Running", "Degraded", "Pending", "Stopped"
	Pods              []AppPod `json:"pods"`
}

// TenantUser represents a Keycloak tenant user
type TenantUser struct {
	Username  string   `json:"username"`
	Email     string   `json:"email"`
	Role      string   `json:"role"`
	Enabled   bool     `json:"enabled"`
	Namespace string   `json:"namespace,omitempty"`
	Groups    []string `json:"groups,omitempty"`
}

// ClusterEvent represents a warning or error event in the cluster
type ClusterEvent struct {
	Namespace string `json:"namespace"`
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Object    string `json:"object"`
	Age       string `json:"age"`
}

// NewServer initializes the Admin Dashboard server
func NewServer(preferredPort int) (*Server, error) {
	if preferredPort <= 0 {
		preferredPort = 4202
	}

	nodeHost := k8s.ResolveClusterNodeIP(context.Background())
	if nodeHost == "" {
		nodeHost = "127.0.0.1"
	}

	var listener net.Listener
	var actualPort int
	for p := preferredPort; p < preferredPort+50; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p))
		if err == nil {
			listener = l
			actualPort = p
			break
		}
	}

	if listener == nil {
		return nil, fmt.Errorf("could not find an available port starting from %d", preferredPort)
	}

	s := &Server{
		port:     actualPort,
		listener: listener,
		nodeHost: nodeHost,
	}

	mux := http.NewServeMux()

	// UI
	mux.HandleFunc("/", s.handleUI)

	// API Endpoints
	mux.HandleFunc("/api/cluster", s.handleCluster)
	mux.HandleFunc("/api/apps", s.handleApps)
	mux.HandleFunc("/api/apps/restart", s.handleAppRestart)
	mux.HandleFunc("/api/apps/scale", s.handleAppScale)
	mux.HandleFunc("/api/apps/delete", s.handleAppDelete)
	mux.HandleFunc("/api/apps/logs", s.handleAppLogs)
	mux.HandleFunc("/api/users", s.handleUsers)
	mux.HandleFunc("/api/users/create", s.handleUserCreate)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/system/cleanup", s.handleSystemCleanup)

	s.httpServer = &http.Server{
		Handler:      s.corsMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s, nil
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Start launches the admin server in the background
func (s *Server) Start() error {
	go func() {
		_ = s.httpServer.Serve(s.listener)
	}()
	return nil
}

// Stop shuts down the server
func (s *Server) Stop(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// URL returns the local dashboard URL
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

// Port returns bound port
func (s *Server) Port() int {
	return s.port
}

// OpenBrowser opens the user's default browser
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

// ─── API Handlers ────────────────────────────────────────────────────────────

func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	k8s.EnsureK3sContext(ctx)

	cmd := exec.CommandContext(ctx, "kubectl", "get", "nodes", "-o", "json")
	out, err := cmd.Output()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "failed to query nodes: %v"}`, err), http.StatusInternalServerError)
		return
	}

	var nodeData struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				NodeInfo struct {
					KubeletVersion   string `json:"kubeletVersion"`
					OSImage          string `json:"osImage"`
					KernelVersion    string `json:"kernelVersion"`
					ContainerRuntime string `json:"containerRuntimeVersion"`
				} `json:"nodeInfo"`
				Addresses []struct {
					Type    string `json:"type"`
					Address string `json:"address"`
				} `json:"addresses"`
				Capacity struct {
					CPU    string `json:"cpu"`
					Memory string `json:"memory"`
					Pods   string `json:"pods"`
				} `json:"capacity"`
				Conditions []struct {
					Type    string `json:"type"`
					Status  string `json:"status"`
					Message string `json:"message"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.Unmarshal(out, &nodeData); err != nil || len(nodeData.Items) == 0 {
		http.Error(w, `{"error": "no nodes found in cluster"}`, http.StatusInternalServerError)
		return
	}

	n := nodeData.Items[0]
	intIP, extIP := "", ""
	for _, addr := range n.Status.Addresses {
		if addr.Type == "InternalIP" {
			intIP = addr.Address
		}
		if addr.Type == "ExternalIP" {
			extIP = addr.Address
		}
	}
	if extIP == "" {
		extIP = s.nodeHost
	}

	nodeStatus := "Ready"
	conds := make(map[string]string)
	for _, c := range n.Status.Conditions {
		conds[c.Type] = c.Status
		if c.Type == "Ready" && c.Status != "True" {
			nodeStatus = "NotReady"
		}
	}

	// Count total pods and namespaces
	totalPods := 0
	if pOut, err := exec.CommandContext(ctx, "kubectl", "get", "pods", "-A", "--no-headers").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(pOut)), "\n")
		if len(lines) > 0 && lines[0] != "" {
			totalPods = len(lines)
		}
	}

	totalNamespaces := 0
	if nsOut, err := exec.CommandContext(ctx, "kubectl", "get", "ns", "--no-headers").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(nsOut)), "\n")
		if len(lines) > 0 && lines[0] != "" {
			totalNamespaces = len(lines)
		}
	}

	res := map[string]any{
		"node": ClusterNode{
			Name:             n.Metadata.Name,
			Status:           nodeStatus,
			Roles:            "control-plane",
			Version:          n.Status.NodeInfo.KubeletVersion,
			InternalIP:       intIP,
			ExternalIP:       extIP,
			OSImage:          n.Status.NodeInfo.OSImage,
			KernelVersion:    n.Status.NodeInfo.KernelVersion,
			ContainerRuntime: n.Status.NodeInfo.ContainerRuntime,
			CPUCapacity:      n.Status.Capacity.CPU,
			MemCapacity:      n.Status.Capacity.Memory,
			PodsCapacity:     n.Status.Capacity.Pods,
			Conditions:       conds,
		},
		"totalPods":       totalPods,
		"totalNamespaces": totalNamespaces,
		"nodeHost":        s.nodeHost,
		"keycloakUrl":     k8s.GetKeycloakBaseURL(ctx),
	}

	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	k8s.EnsureK3sContext(ctx)

	var (
		depOut  []byte
		svcOut  []byte
		podsOut []byte
		nsOut   []byte
		wg      sync.WaitGroup
	)

	wg.Add(4)
	go func() {
		defer wg.Done()
		depCmd := exec.CommandContext(ctx, "kubectl", "get", "deployments", "-A", "-o", "json")
		depOut, _ = depCmd.Output()
	}()
	go func() {
		defer wg.Done()
		svcCmd := exec.CommandContext(ctx, "kubectl", "get", "svc", "-A", "-o", "json")
		svcOut, _ = svcCmd.Output()
	}()
	go func() {
		defer wg.Done()
		podsCmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-A", "-o", "json")
		podsOut, _ = podsCmd.Output()
	}()
	go func() {
		defer wg.Done()
		nsCmd := exec.CommandContext(ctx, "kubectl", "get", "namespaces", "-o", "json")
		nsOut, _ = nsCmd.Output()
	}()
	wg.Wait()

	// Parse Namespace owners
	nsOwners := make(map[string]string)
	var nsList struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(nsOut, &nsList); err == nil {
		for _, item := range nsList.Items {
			owner := item.Metadata.Labels["idlistack.io/owner"]
			if owner == "" {
				owner = "developer"
			}
			nsOwners[item.Metadata.Name] = owner
		}
	}

	// Parse Services by namespace
	type svcPort struct {
		port     int
		nodePort int
	}
	nsServices := make(map[string][]svcPort)
	var svcList struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Ports []struct {
					Port     int `json:"port"`
					NodePort int `json:"nodePort"`
				} `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(svcOut, &svcList); err == nil {
		for _, item := range svcList.Items {
			for _, p := range item.Spec.Ports {
				nsServices[item.Metadata.Namespace] = append(nsServices[item.Metadata.Namespace], svcPort{
					port:     p.Port,
					nodePort: p.NodePort,
				})
			}
		}
	}

	// Parse Pods by namespace
	nsPods := make(map[string][]AppPod)
	var podList struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
			Status struct {
				Phase             string `json:"phase"`
				PodIP             string `json:"podIP"`
				ContainerStatuses []struct {
					Ready        bool `json:"ready"`
					RestartCount int  `json:"restartCount"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(podsOut, &podList); err == nil {
		for _, p := range podList.Items {
			restarts := 0
			ready := false
			for _, cs := range p.Status.ContainerStatuses {
				if cs.Ready {
					ready = true
				}
				restarts += cs.RestartCount
			}
			nsPods[p.Metadata.Namespace] = append(nsPods[p.Metadata.Namespace], AppPod{
				Name:       p.Metadata.Name,
				Phase:      p.Status.Phase,
				Ready:      ready,
				Restarts:   restarts,
				PodIP:      p.Status.PodIP,
				NodeName:   p.Spec.NodeName,
				StatusText: p.Status.Phase,
			})
		}
	}

	// Parse Deployments and build AppInfo
	var apps []AppInfo
	var depList struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				Namespace         string `json:"namespace"`
				CreationTimestamp string `json:"creationTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Replicas int `json:"replicas"`
				Template struct {
					Spec struct {
						Containers []struct {
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				ReadyReplicas     int `json:"readyReplicas"`
				AvailableReplicas int `json:"availableReplicas"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.Unmarshal(depOut, &depList); err == nil {
		for _, d := range depList.Items {
			ns := d.Metadata.Namespace
			// Only include user tenant namespaces or idlistack namespaces
			if strings.HasPrefix(ns, "kube-") {
				continue
			}

			appName := d.Metadata.Name
			owner := nsOwners[ns]
			if owner == "" {
				owner = "developer"
			}

			app := AppInfo{
				Name:              appName,
				Namespace:         ns,
				Owner:             owner,
				ReplicasDesired:   d.Spec.Replicas,
				ReplicasReady:     d.Status.ReadyReplicas,
				ReplicasAvailable: d.Status.AvailableReplicas,
				Created:           d.Metadata.CreationTimestamp,
				Status:            "Running",
				Pods:              nsPods[ns],
			}

			if len(d.Spec.Template.Spec.Containers) > 0 {
				app.Image = d.Spec.Template.Spec.Containers[0].Image
			}

			if app.ReplicasDesired == 0 {
				app.Status = "Stopped"
			} else if app.ReplicasReady == app.ReplicasDesired {
				app.Status = "Running"
			} else if app.ReplicasReady > 0 {
				app.Status = "Degraded"
			} else {
				app.Status = "Pending"
			}

			// Service NodePort
			for _, sp := range nsServices[ns] {
				if sp.nodePort > 0 {
					app.NodePort = sp.nodePort
					app.URL = fmt.Sprintf("http://%s:%d", s.nodeHost, sp.nodePort)
					break
				}
			}

			apps = append(apps, app)
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"apps": apps,
	})
}

func (s *Server) handleAppRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Namespace string `json:"namespace"`
		App       string `json:"app"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "rollout", "restart", fmt.Sprintf("deployment/%s", req.App), "-n", req.Namespace)
	out, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, strings.TrimSpace(string(out))), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": "Rollout restart initiated"})
}

func (s *Server) handleAppScale(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Namespace string `json:"namespace"`
		App       string `json:"app"`
		Replicas  int    `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "scale", fmt.Sprintf("deployment/%s", req.App), fmt.Sprintf("--replicas=%d", req.Replicas), "-n", req.Namespace)
	out, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, strings.TrimSpace(string(out))), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "replicas": req.Replicas})
}

func (s *Server) handleAppDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Namespace string `json:"namespace"`
		App       string `json:"app"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 1. Uninstall Helm release
	_ = exec.CommandContext(ctx, "helm", "uninstall", req.App, "-n", req.Namespace).Run()

	// 2. Delete Namespace
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "namespace", req.Namespace, "--wait=false")
	out, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, strings.TrimSpace(string(out))), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": fmt.Sprintf("Application and namespace %s deleted", req.Namespace)})
}

func (s *Server) handleAppLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ns := r.URL.Query().Get("namespace")
	app := r.URL.Query().Get("app")
	tailStr := r.URL.Query().Get("tail")
	tail := 100
	if t, err := strconv.Atoi(tailStr); err == nil && t > 0 {
		tail = t
	}

	if ns == "" || app == "" {
		http.Error(w, `{"error": "namespace and app parameters required"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "logs", "-n", ns, fmt.Sprintf("deployment/%s", app), fmt.Sprintf("--tail=%d", tail), "--all-containers=true")
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		http.Error(w, fmt.Sprintf(`{"error": "Failed to fetch logs: %v"}`, err), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"logs": string(out),
	})
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var users []TenantUser
	existingUsernames := make(map[string]bool)

	// Fetch users directly from Keycloak
	kcUsers, err := auth.ListKeycloakUsers(ctx)
	if err == nil {
		for _, ku := range kcUsers {
			role := "developer"
			for _, g := range ku.Groups {
				if g == "cluster-admins" {
					role = "cluster-admin"
				}
			}
			if ku.Username == "admin" {
				role = "cluster-admin"
			}
			users = append(users, TenantUser{
				Username: ku.Username,
				Email:    ku.Email,
				Role:     role,
				Enabled:  ku.Enabled,
				Groups:   ku.Groups,
			})
			existingUsernames[ku.Username] = true
		}
	}

	// Always ensure admin user is listed
	if !existingUsernames["admin"] {
		users = append([]TenantUser{{
			Username: "admin",
			Email:    "admin@idlistack.local",
			Role:     "cluster-admin",
			Enabled:  true,
			Groups:   []string{"CLI - Deploy", "cluster-admins"},
		}}, users...)
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"users": users,
	})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username  string `json:"username"`
		Email     string `json:"email"`
		Password  string `json:"password"`
		Role      string `json:"role"`
		Namespace string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if req.Username == "" || req.Password == "" {
		http.Error(w, `{"error": "Username and password are required"}`, http.StatusBadRequest)
		return
	}

	if req.Role == "" {
		req.Role = "developer"
	}

	err := auth.CreateKeycloakUser(ctx, req.Username, req.Email, req.Username, "User", req.Password, req.Role, req.Namespace)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":  true,
		"username": req.Username,
		"role":     req.Role,
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "get", "events", "-A", "--field-selector", "type=Warning", "--sort-by=.metadata.creationTimestamp", "-o", "json")
	out, err := cmd.Output()
	var events []ClusterEvent

	if err == nil {
		var evList struct {
			Items []struct {
				InvolvedObject struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"involvedObject"`
				Reason            string `json:"reason"`
				Message           string `json:"message"`
				Type              string `json:"type"`
				Metadata struct {
					Namespace string `json:"namespace"`
				} `json:"metadata"`
				LastTimestamp string `json:"lastTimestamp"`
			} `json:"items"`
		}
		if err := json.Unmarshal(out, &evList); err == nil {
			for _, item := range evList.Items {
				events = append(events, ClusterEvent{
					Namespace: item.Metadata.Namespace,
					Type:      item.Type,
					Reason:    item.Reason,
					Message:   item.Message,
					Object:    fmt.Sprintf("%s/%s", item.InvolvedObject.Kind, item.InvolvedObject.Name),
					Age:       item.LastTimestamp,
				})
			}
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"events": events,
	})
}

func (s *Server) handleSystemCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// List helm releases in failed or stuck states
	cmd := exec.CommandContext(ctx, "helm", "list", "-A", "--pending", "--failed", "-o", "json")
	var cleaned []string
	if out, err := cmd.Output(); err == nil {
		var releases []struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		}
		if err := json.Unmarshal(out, &releases); err == nil {
			for _, rel := range releases {
				_ = exec.CommandContext(ctx, "helm", "uninstall", rel.Name, "-n", rel.Namespace).Run()
				cleaned = append(cleaned, fmt.Sprintf("%s (%s)", rel.Name, rel.Namespace))
			}
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"cleaned": cleaned,
	})
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	io.WriteString(w, RenderAdminHTML(s.nodeHost, s.port))
}
