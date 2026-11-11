# IdliStack CLI — Production Architecture & Technical Reference

> **Deploy any application with zero configuration and zero-trust RBAC.**
> Designed for local environments and edge Kubernetes clusters. Powered by Go, Frappe Auth Control Plane, Teleport M2M, Railpack (BuildKit), Docker, Helm v3, and K3s.

---

## 1. Executive Summary & Design Philosophy

IdliStack is a production-grade deployment orchestrator that abstracts the complexities of containerization, Kubernetes networking, and cluster security. It provides an invisible, highly resilient pipeline that automatically **authenticates** developers through a centralized **Frappe Auth Control Plane**, **mints ephemeral zero-trust Kubernetes credentials** via **Teleport Machine-to-Machine (M2M)**, **detects** an application's technology stack, **compiles** an optimized OCI-compliant image via **Railpack** (BuildKit), and **deploys** it to a lightweight **K3s** cluster using native **Helm** charts with persistent storage and companion database sidecars.

### Core Tenets
- **Zero Configuration:** Developers never need to write `Dockerfiles`, Kubernetes YAML manifests, or CI/CD pipelines to achieve a production-grade deployment.
- **Zero-Trust Access Control:** Developers never receive cluster-admin or permanent kubeconfigs. Every deployment mints a short-lived (15-minute), namespace-restricted X.509 client certificate scoped strictly to their application (`idlistack-<app>`).
- **Dynamic Adaptability:** Hardcoded versions are anti-patterns. IdliStack dynamically infers runtimes from source code (e.g., `package.json`, `go.mod`, `.python-version`, `Cargo.toml`).
- **Zero-Install Client Resilience:** End users do not need to pre-install Railpack. IdliStack's standalone resolver automatically fetches the binary transparently into `~/.idlistack/bin/railpack`, with a built-in multi-stage Dockerfile generator as an offline safety net.
- **Production-Grade Tooling:** IdliStack utilizes CNCF-certified tools (**K3s**, **Helm v3**, **BuildKit**, **Teleport**) to ensure 100% parity with cloud production environments.

---

## 2. Technology Stack & Subsystems

| Component | Technology | Role & Purpose |
| :--- | :--- | :--- |
| **CLI Framework** | [Cobra](https://github.com/spf13/cobra) (Go) | Command parsing, flags, subcommands (`up`, `down`, `status`, `admin`, `login`, `whoami`, `env`, `init`). |
| **Auth Control Plane** | [Frappe Framework](https://frappeframework.com) | User directory, tenant ownership, role assignment, audit logging, and Teleport delegation (`idli_cli` app on port 8000). |
| **Zero-Trust Gateway** | [Teleport v16](https://goteleport.com) (Auth & Proxy) | Issues cryptographic, short-lived X.509 certificates and enforces Kubernetes RBAC (`idlistack-cluster`). |
| **Machine-to-Machine Bot** | Teleport `tbot` / `tctl` | Dedicated bot identity (`bot-frappe-deployer`) enabling Frappe to safely mint credentials without exposing root CA keys. |
| **Primary Build Engine** | [Railpack](https://railpack.com) (v0.39+) | Modern BuildKit-based planner & OCI image compiler by Railway. |
| **Detection Engine** | Railpack + Dynamic Rules | Stack, runtime version, and start command inference with heuristic fallback. |
| **Image Builder** | Docker Engine & BuildKit | Compiles inferred architectures into OCI-compliant images with multi-tier caching. |
| **Cluster Runtime** | K3s (containerd) | Lightweight, single-binary Kubernetes distribution with direct image sideloading. |
| **Orchestrator** | Helm v3 | Scaffolds and manages Kubernetes resources atomically (deployments, rollbacks, lifecycle hooks). |
| **Developer Console** | Embedded HTTP & SSE | Embedded Go web server providing real-time metrics, live log streaming, container shell, and backups (port 4200). |
| **Admin Console** | Embedded Go HTTP Server | Cluster-wide resource allocation, multi-tenant namespace overview, and IAM status (port 4202). |

---

## 3. High-Level Architectural Diagram

```
┌──────────────────────────────────────────────────────────────────────────────────┐
│                               DEVELOPER WORKSPACE                                │
│   ┌───────────────────────────┐           ┌──────────────────────────────────┐   │
│   │     IdliStack CLI         │           │   VS Code Extension (v1.14.0)    │   │
│   │  (idlistack up, status)   │           │   (Status bar, 1-Click Deploy)   │   │
│   └─────────────┬─────────────┘           └────────────────┬─────────────────┘   │
└─────────────────┼──────────────────────────────────────────┼─────────────────────┘
                  │                                          │
                  │ 1. Browser Login (`http://localhost:8000/cli_login`)
                  ▼                                          ▼
┌──────────────────────────────────────────────────────────────────────────────────┐
│                      FRAPPE CONTROL PLANE (`idli_cli` on port 8000)              │
│  - User Directory & Team RBAC (e.g., son@gmail.com)                              │
│  - Multi-tenant Namespace Registry (`idlistack-<app>`)                           │
│  - Deployment Audit Trail & Status History                                       │
└─────────────────────────┬────────────────────────────────────────────────────────┘
                          │
                          │ 2. Request Ephemeral Scoped Kubeconfig
                          │    via Machine-to-Machine Identity (`bot-frappe-deployer`)
                          ▼
┌──────────────────────────────────────────────────────────────────────────────────┐
│                      TELEPORT ZERO-TRUST GATEWAY (v16.2.0)                       │
│  - Auth Service (port 3025) & Web Proxy (port 3080)                              │
│  - Signs temporary X.509 client certificate (15-minute TTL)                      │
│  - Enforces Kubernetes RBAC Role: idlistack-developer                            │
│  - Restricts access strictly to target namespace: idlistack-<app>                │
└─────────────────────────┬────────────────────────────────────────────────────────┘
                          │
                          │ 3. Ephemeral Kubeconfig returned to CLI (mode 0600)
                          │    Active KUBECONFIG exported, memory shredding on exit
                          ▼
┌──────────────────────────────────────────────────────────────────────────────────┐
│                                 K3S LOCAL CLUSTER                                │
│  - Kube-API Server (https://127.0.0.1:6443)                                      │
│  - Sideloaded containerd image cache (`k3s ctr images import`)                   │
│  - Namespace: `idlistack-<app>`                                                  │
│    ├── Pod: Application (Port: 3000 -> NodePort: 30000-32767)                   │
│    ├── Pod: Companion Database (PostgreSQL 15 / MariaDB 10.6 / Redis 7)          │
│    ├── Service: NodePort deterministic router                                    │
│    └── PVC: Persistent local storage                                             │
└──────────────────────────────────────────────────────────────────────────────────┘
```

---

## 4. The 6-Stage Deployment Pipeline

Every execution of `idlistack up` runs through a deterministic 6-stage lifecycle:

```
┌────────────────────────────────────────────────────────────────────────────────────┐
│                                    idlistack up                                    │
├──────────────┬──────────────┬──────────────┬──────────────┬──────────────┬─────────┤
│   Stage 1    │   Stage 2    │   Stage 3    │   Stage 4    │   Stage 5    │ Stage 6 │
│   Validate   │    Detect    │  Save Build  │  Build OCI   │   Sideload   │  Helm   │
│ & Authorize  │ Application  │    Plan      │ via Railpack │    to K3s    │ Deploy  │
└──────────────┴──────────────┴──────────────┴──────────────┴──────────────┴─────────┘
```

### 4.1. Stage 1: Workspace Validation & Zero-Trust Teleport Authorization
1. **Config Verification:** Loads `idlistack.toml`. If absent, auto-generates a minimal configuration or prompts via `idlistack init`.
2. **Ignore Engine:** Recursively scans the directory using `.idlistackignore` (and `.gitignore` as fallback). Excludes heavy artifacts (`node_modules`, `.git`, `__pycache__`, `.venv`, `target`, `dist`).
3. **Dynamic Size Ceiling:** Enforces `currentSize + 50MB` safety buffer to prevent accidental disk saturation.
4. **Frappe Session Validation:** Reads `~/.idlistack/credentials.json`. Verifies the developer's session token against Frappe (`/api/method/idli_cli.api.auth.verify_session`).
5. **Namespace Claiming & Authorization:**
   - The CLI requests an ephemeral Kubeconfig for namespace `idlistack-<app>` via `/api/method/idli_cli.api.deploy.request_kubeconfig`.
   - Frappe checks if the namespace is owned by the developer. If unassigned or seeded by `Administrator`, Frappe automatically assigns ownership to the developer.
6. **Teleport M2M Kubeconfig Minting:**
   - Frappe uses the Teleport bot identity (`bot-frappe-deployer`) to request a short-lived (900 seconds / 15 minutes) certificate scoped exclusively to the target namespace.
   - CLI writes the decoded kubeconfig to `/tmp/idlistack-kube/kc-<app>-<random>.yaml` with permissions `0600`.
   - CLI sets `os.Setenv("KUBECONFIG", path)` and registers an exit handler that zero-fills the file before deletion.

### 4.2. Stage 2: Multi-Layer Detection Engine
The detection engine inspects the codebase using a deterministic priority hierarchy:

```
┌─────────────────────────────────────────────────────────────┐
│ Layer 0: Existing Dockerfile & docker-compose.yml           │
├─────────────────────────────────────────────────────────────┤
│ Layer 0.5: Dynamic Specialized Rules (Ghost, Frappe, etc.)  │
├─────────────────────────────────────────────────────────────┤
│ Layer 1: Railpack Detection Engine (Primary v0.39+)         │
├─────────────────────────────────────────────────────────────┤
│ Layer 2: Deep Heuristic Recursive Fallback (Safety Net)     │
└─────────────────────────────────────────────────────────────┘
```

- **Transparent Railpack Resolution:** Uses `ResolveRailpackBinary()`. If `railpack` is not in the system `PATH`, it checks `~/.idlistack/bin/railpack` or automatically downloads the standalone static binary for the host OS and architecture.
- **Runtime Inference:** Reads version files (e.g., `.nvmrc`, `package.json -> engines.node`, `go.mod`, `.python-version`, `Cargo.toml`).
- **Port Detection:** Inspects environment files, framework config, or defaults to stack conventions (Node: 3000, Python: 8000, Go: 8080).

### 4.3. Stage 3: Project State & Build Plan Serialization
- Serializes the normalized build plan and project state into a single **`.idlistack.json`** file in the project root.
- Eliminates nested folder clutter in developer repositories while maintaining 100% deterministic reproducibility.
- When invoked as `idlistack up --inspect`, outputs formatted JSON and confidence ratings without triggering a build or deployment.

### 4.4. Stage 4: OCI Image Compilation
- **Tag Generation:** Generates a deterministic tag: `idlistack/<project-name>:<unix-timestamp>`.
- **Railpack Compilation:** Triggers `railpack build . --name <imageTag>` utilizing Docker BuildKit caching mounts.
- **Native Fallback:** If Railpack is unavailable, generates an optimized multi-stage Dockerfile in a secure temporary file and builds directly via `docker build`, cleaning up the file upon completion.

### 4.5. Stage 5: Cluster Detection & Image Sideloading
To bypass the latency, network cost, and credential management of public Docker registries, IdliStack injects images directly into the local cluster:
- **Cluster Discovery:** Detects K3s via `kubectl config current-context` (or active Teleport kubeconfig) and node metadata.
- **K3s containerd Import:** Exports the image to `/tmp/idlistack-<time>.tar` and imports directly via `k3s ctr images import`.
- **Sidecar Pre-loading:** Pulls and sideloads companion dependency images (`postgres:15-alpine`, `mariadb:10.6`, `redis:7-alpine`, `busybox:1.36`).

### 4.6. Stage 6: Helm Orchestration & Rollout
- **Concurrency Locking:** Acquires `/tmp/idlistack-<app>.lock` via `syscall.Flock` to prevent simultaneous conflicting deployments.
- **Transient Chart Generation:** Generates atomic Helm manifests into a transient `/tmp/idlistack-helm-<app>-*` directory.
- **Zombie Release Purging:** Identifies and cleans releases stuck in `pending-install`, `pending-upgrade`, or `failed`.
- **Atomic Deployment:** Executes:
  ```bash
  helm upgrade --install <app> <transientHelmDir> --namespace idlistack-<app> --create-namespace --wait --timeout 300s --atomic
  ```
- **Automatic Cleanup:** Removes transient chart artifacts immediately on rollout completion.
- **TCP Socket Health Probes:** Applies a **150-second startup grace window** (30 attempts × 5s) to allow migrations or heavy frameworks to boot cleanly.
- **Audit Logging:** Reports deployment outcome, image tag, and live URL back to Frappe via `/api/method/idli_cli.api.deploy.record_deployment`.

---

## 5. Security Architecture: Teleport M2M & Zero-Trust RBAC

### 5.1. The Threat Model
In multi-developer environments or local shared workstations, giving developers unrestricted `cluster-admin` access creates serious risks:
- Developers can accidentally delete or tamper with system namespaces (`kube-system`, `teleport`).
- Developers can view secrets belonging to other projects.
- Credentials stored on disk can be leaked or stolen.

### 5.2. Teleport Machine-to-Machine (M2M) Solution
IdliStack solves this using Teleport's Machine-to-Machine bot architecture:

1. **Bot Provisioning:** Teleport runs a dedicated service identity `bot-frappe-deployer` with the `idlistack-bot` role.
2. **Ephemeral Minting:** When Frappe receives an authorized deployment request, it asks Teleport to mint an X.509 certificate with:
   - **TTL:** 15 minutes (900 seconds).
   - **Allowed Kubernetes Groups:** `idlistack:developer`.
   - **Allowed Kubernetes Namespaces:** Only `idlistack-<app>`.
3. **No Long-Lived Cluster Secrets:** Developers never possess a cluster CA certificate or admin token. If a laptop is left unattended or a temporary file is inspected later, the token has already expired.
4. **Memory & Disk Sanitization:** The CLI registers process signal handlers (`SIGINT`, `SIGTERM`, normal exit). When the CLI finishes or is cancelled, it overwrites the ephemeral file with zeroes before unlinking it.

---

## 6. End-to-End Real-World Lifecycle Walkthrough

Here is a step-by-step example showing exactly what happens under the hood when a developer builds and deploys an application from scratch.

### Step 1: Initial Setup & Workspace Creation
A developer creates a new Node.js project:
```bash
mkdir my-api && cd my-api
npm init -y
npm install express
cat << 'EOF' > server.js
const express = require('express');
const app = express();
const port = process.env.PORT || 3000;
app.get('/', (req, res) => res.json({ status: 'ok', message: 'Hello from IdliStack!' }));
app.listen(port, () => console.log(`Server listening on port ${port}`));
EOF
```

### Step 2: Developer Authentication (`idlistack login`)
The developer logs in:
```bash
idlistack login
```
1. CLI starts a temporary HTTP server on `http://127.0.0.1:4201/callback`.
2. Automatically opens the developer's default browser to `http://localhost:8000/cli_login`.
3. Developer logs in with their team account (`son@gmail.com`).
4. Frappe verifies the user, generates a secure CLI token, and redirects back to `http://127.0.0.1:4201/callback?token=...`.
5. CLI saves the session to `~/.idlistack/credentials.json`.

### Step 3: Verifying Session Scope (`idlistack whoami`)
```bash
idlistack whoami
```
Outputs:
```text
  Authenticated as: son@gmail.com
  Frappe Control Plane: http://localhost:8000
  Teleport RBAC Scope: Namespace-Restricted
  Authorized Namespaces: idlistack-my-api
```

### Step 4: Zero-Config Deployment (`idlistack up`)
The developer deploys with one command:
```bash
idlistack up
```

**Lifecycle execution:**
1. **Validation:** Identifies workspace directory `my-api`. Filters out `node_modules` (1.2 MB transferred, ceiling set to 51.2 MB).
2. **Teleport Scoping:** Sends request to Frappe. Frappe issues an ephemeral Kubeconfig for namespace `idlistack-my-api`.
3. **Detection:** Railpack scans `package.json`, detects `node (v24.21.0)`, detects start script `node server.js`, detects port `3000`.
4. **Build:** Railpack invokes Docker BuildKit. Layers are compiled and tagged `idlistack/my-api:1728892100`.
5. **Sideload:** CLI saves tar stream directly into K3s containerd namespace `k8s.io`.
6. **Helm Rollout:**
   - Evaluates deterministic NodePort: `32249`.
   - Generates transient Helm chart in `/tmp/idlistack-helm-my-api-*`.
   - Executes atomic upgrade: `helm upgrade --install my-api <transientHelmDir> -n idlistack-my-api --atomic`.
   - Automatically cleans up transient chart files, persisting state to `.idlistack.json`.
   - Waits for TCP socket probe on port 3000 to become healthy.
7. **Success Output:**
   ```text
   ✓ Application deployed successfully!
     URL: http://127.0.0.1:32249
     Namespace: idlistack-my-api
   ```

### Step 5: Live Monitoring & Management (`idlistack status`)
```bash
idlistack status
```
- Launches the embedded Go web server on `http://127.0.0.1:4200`.
- Browser displays:
  - Real-time CPU and Memory usage graphs.
  - Interactive terminal shell inside the running container.
  - Secret & environment variable editor with zero-downtime rolling restart.
  - 1-click database SQL backups.
  - Helm revision history with 1-click rollback.

### Step 6: Cluster Administration (`idlistack admin`)
```bash
idlistack admin
```
- Launches the cluster admin console on `http://127.0.0.1:4202`.
- Displays aggregate CPU/RAM allocation, active tenant namespaces, and live pod counts.

### Step 7: Teardown (`idlistack down`)
```bash
idlistack down
```
- Uninstalls Helm release `my-api`.
- Cleans up persistent storage and deletes namespace `idlistack-my-api`.

---

## 7. Directory Structure & Code Organization

```text
CLI/
├── main.go                          # CLI entry point
├── Makefile                         # Build targets (build, install, clean, test)
├── PRODUCTION_ARCHITECTURE.md       # Technical architecture specification
├── README.md                        # Primary user documentation
├── cmd/                             # Cobra command implementations
│   ├── root.go                      # Global flags and root command context
│   ├── auth.go                      # idlistack login, logout, whoami
│   ├── admin.go                     # idlistack admin console (port 4202)
│   ├── init.go                      # Project initialization
│   ├── up.go                        # The 6-stage deployment pipeline orchestrator
│   ├── down.go                      # Helm uninstall and namespace teardown
│   ├── logs.go                      # Real-time streaming log aggregator
│   ├── status.go                    # Terminal health check & web dashboard launcher (port 4200)
│   └── env.go                       # Kubernetes Secret management
├── internal/                        # Core internal engine logic
│   ├── admin/                       # Multi-tenant admin server & HTML assets
│   ├── app/                         # Fast in-memory filesystem scanner & ignore rules
│   ├── auth/                        # Frappe API client & Teleport ephemeral kubeconfig generator
│   │   └── frappe.go                # Browser login, token management, and cleanup handlers
│   ├── buildplan/                   # Standardized build plan schema
│   ├── config/                      # TOML configuration parser
│   ├── dashboard/                   # Embedded developer Web Dashboard server & static assets
│   ├── detect/                      # Multi-layer detection engine
│   │   ├── detect.go                # Detection orchestrator & layer priority
│   │   ├── railpack.go              # Railpack v0.39+ resolver, downloader & parser
│   │   ├── rules.go                 # Dynamic framework rules engine
│   │   └── frameworks.json          # Specialized framework signatures
│   ├── k8s/                         # Kubernetes logic
│   │   ├── deployer.go              # Helm chart generator, dependencies, and rollout
│   │   └── rbac.go                  # Context switcher & K3s node IP resolver
│   └── ui/                          # Terminal UI components and banners
└── vscode-extension/                # VS Code Marketplace Extension
    ├── package.json                 # Extension manifest (v1.14.0)
    ├── extension.js                 # VS Code extension controller & status bar
    ├── README.md                    # Extension documentation
    ├── bin/                         # Bundled idlistack executable
    └── idlistack-vscode-1.14.0.vsix # Packaged extension bundle
```

---

## 8. Operational Runbook & Troubleshooting

### Teleport Management
```bash
# Check Teleport cluster health
tctl status

# Verify Kubernetes cluster registration
tctl kube ls

# Inspect active Machine-to-Machine bots
tctl bots ls

# List Teleport roles and users
tctl users ls
```

### K3s Containerd Inspection
```bash
# List all sideloaded images inside K3s
sudo k3s crictl images

# List active containers
sudo k3s crictl ps

# Inspect logs of a specific container directly
sudo k3s crictl logs <container-id>
```

### Teleport Service Management
Teleport runs as a systemd user service:
```bash
# Check status
systemctl --user status teleport.service

# Restart Teleport
systemctl --user restart teleport.service

# View Teleport logs
journalctl --user -u teleport.service -f
```

---

## 9. Conclusion

The IdliStack architecture bridges the gap between modern zero-config developer platforms and enterprise-grade Kubernetes operations. By integrating **Railpack** for container builds, **K3s containerd** for instant local sideloading, **Helm v3** for atomic lifecycle management, and **Teleport M2M** for Zero-Trust RBAC under a unified **Frappe Control Plane**, IdliStack delivers uncompromised developer speed with production-grade security guarantees.
