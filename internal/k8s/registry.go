package k8s

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/idlistack/cli/internal/ui"
)

const vendorRegistryManifest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: vendor-registry
  namespace: %[1]s
  labels:
    app: vendor-registry
spec:
  replicas: 1
  selector:
    matchLabels:
      app: vendor-registry
  template:
    metadata:
      labels:
        app: vendor-registry
    spec:
      containers:
      - name: registry
        image: registry:2
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 5000
        env:
        - name: REGISTRY_STORAGE_DELETE_ENABLED
          value: "true"
---
apiVersion: v1
kind: Service
metadata:
  name: vendor-registry
  namespace: %[1]s
  labels:
    app: vendor-registry
spec:
  type: NodePort
  selector:
    app: vendor-registry
  ports:
  - port: 5000
    targetPort: 5000
`

func kubectlCmd(ctx context.Context, args ...string) *exec.Cmd {
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		args = append([]string{"--kubeconfig=" + kc}, args...)
	} else {
		home, _ := os.UserHomeDir()
		vendorKc := filepath.Join(home, ".kube", "whatomate-vendor-kubeconfig.yaml")
		if _, err := os.Stat(vendorKc); err == nil {
			args = append([]string{"--kubeconfig=" + vendorKc}, args...)
		}
	}
	return exec.CommandContext(ctx, "kubectl", args...)
}

// EnsureInClusterRegistry verifies that an in-cluster OCI registry exists in the target namespace.
// If missing, it deploys a lightweight registry:2 service and returns the allocated NodePort.
func EnsureInClusterRegistry(ctx context.Context, namespace string) (string, int, error) {
	// 1. Try to find existing registry service
	svcCandidates := []string{"vendor-registry", "registry"}
	for _, svc := range svcCandidates {
		cmd := kubectlCmd(ctx, "get", "svc", svc, "-n", namespace, "-o", "jsonpath={.spec.ports[0].nodePort}")
		if out, err := cmd.Output(); err == nil {
			portStr := strings.TrimSpace(string(out))
			if p, convErr := strconv.Atoi(portStr); convErr == nil && p >= 30000 && p <= 32767 {
				return svc, p, nil
			}
		}
	}

	// Also check by label app=vendor-registry or app=registry
	cmdLabel := kubectlCmd(ctx, "get", "svc", "-l", "app=vendor-registry", "-n", namespace, "-o", "jsonpath={.items[0].metadata.name} {.items[0].spec.ports[0].nodePort}")
	if out, err := cmdLabel.Output(); err == nil {
		fields := strings.Fields(string(out))
		if len(fields) >= 2 {
			if p, convErr := strconv.Atoi(fields[1]); convErr == nil && p >= 30000 && p <= 32767 {
				return fields[0], p, nil
			}
		}
	}

	// 2. Deploy in-cluster registry if not found
	ui.Detail("Deploying in-cluster private OCI registry in namespace %s...", color.CyanString(namespace))
	manifest := fmt.Sprintf(vendorRegistryManifest, namespace)
	applyCmd := kubectlCmd(ctx, "apply", "-n", namespace, "-f", "-")
	applyCmd.Stdin = strings.NewReader(manifest)
	if out, err := applyCmd.CombinedOutput(); err != nil {
		return "", 0, fmt.Errorf("failed to deploy in-cluster registry: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	// Wait up to 30 seconds for NodePort allocation
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		cmd := kubectlCmd(ctx, "get", "svc", "vendor-registry", "-n", namespace, "-o", "jsonpath={.spec.ports[0].nodePort}")
		if out, err := cmd.Output(); err == nil {
			portStr := strings.TrimSpace(string(out))
			if p, convErr := strconv.Atoi(portStr); convErr == nil && p >= 30000 && p <= 32767 {
				return "vendor-registry", p, nil
			}
		}
	}

	return "", 0, fmt.Errorf("timed out waiting for in-cluster registry service in namespace %s", namespace)
}

func getFreeLocalPort() (int, error) {
	// First check if 5000 is free; preferring 5000 keeps logs familiar
	l, err := net.Listen("tcp", "127.0.0.1:5000")
	if err == nil {
		_ = l.Close()
		return 5000, nil
	}
	// Fall back to ephemeral port
	l, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func isRegistryResponding(port int) bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/v2/", port))
	if err == nil && resp != nil {
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	return false
}

// PushToInClusterRegistry ensures an in-cluster registry is available, establishes a tunnel,
// pushes the built image, and returns the tag for the cluster's kubelet to pull.
func PushToInClusterRegistry(ctx context.Context, namespace, localImageTag, appName, deployHash string) (string, error) {
	svcName, nodePort, err := EnsureInClusterRegistry(ctx, namespace)
	if err != nil {
		return "", err
	}

	ui.Detail("In-cluster registry: %s (NodePort %d)", color.CyanString(svcName), nodePort)

	// Determine local forwarding port
	localPort := 5000
	needPortForward := true

	if isRegistryResponding(5000) {
		// Existing tunnel or registry already accessible on 5000
		ui.Detail("Active registry tunnel detected on port 5000")
		needPortForward = false
	} else {
		port, err := getFreeLocalPort()
		if err != nil {
			return "", fmt.Errorf("failed to allocate local port for tunnel: %w", err)
		}
		localPort = port
	}

	var pfCmd *exec.Cmd
	if needPortForward {
		ui.Detail("Opening tunnel to registry: 127.0.0.1:%d -> svc/%s:5000", localPort, svcName)
		pfCtx, cancelPf := context.WithCancel(ctx)
		defer cancelPf()

		pfCmd = kubectlCmd(pfCtx, "port-forward", fmt.Sprintf("svc/%s", svcName), fmt.Sprintf("%d:5000", localPort), "-n", namespace)
		if err := pfCmd.Start(); err != nil {
			return "", fmt.Errorf("failed to start kubectl port-forward: %w", err)
		}
		defer func() {
			if pfCmd.Process != nil {
				_ = pfCmd.Process.Kill()
				_ = pfCmd.Wait()
			}
		}()

		// Wait for tunnel to be responsive
		ready := false
		waitDeadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(waitDeadline) {
			if isRegistryResponding(localPort) {
				ready = true
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if !ready {
			return "", fmt.Errorf("timed out connecting to in-cluster registry tunnel on port %d", localPort)
		}
	}

	// Tag and push through the tunnel
	tunnelTag := fmt.Sprintf("127.0.0.1:%d/%s:%s", localPort, appName, deployHash)
	ui.Detail("Pushing image to in-cluster registry (%s)...", color.CyanString(tunnelTag))

	tagCmd := exec.CommandContext(ctx, "docker", "tag", localImageTag, tunnelTag)
	if err := tagCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to tag image for in-cluster registry: %w", err)
	}
	defer func() {
		_ = exec.CommandContext(context.Background(), "docker", "rmi", tunnelTag).Run()
	}()

	pushCmd := exec.CommandContext(ctx, "docker", "push", tunnelTag)
	pushCmd.Stdout = os.Stdout
	pushCmd.Stderr = os.Stderr
	if err := pushCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to push image to in-cluster registry: %w", err)
	}

	// Remote pod pulls from node's loopback interface via the NodePort
	remotePullTag := fmt.Sprintf("127.0.0.1:%d/%s:%s", nodePort, appName, deployHash)
	ui.Success(fmt.Sprintf("Image stored in cluster registry: %s", color.CyanString(remotePullTag)))

	return remotePullTag, nil
}
