# IdliStack CLI — Production Architecture & Technical Reference

> **Deploy any application with zero configuration.**
> Designed for local environments and production edge clusters. Powered by Go, Railpack (BuildKit), Docker, Helm, and K3s.

---

## 1. Executive Summary & Design Philosophy

IdliStack is a production-grade deployment orchestrator that abstracts the complexities of containerization and Kubernetes. It acts as an intelligent, invisible pipeline that automatically **detects** an application's stack, **compiles** an optimized OCI-compliant image via **Railpack** (BuildKit), and **deploys** it to a lightweight **K3s** cluster using native **Helm** charts with persistent storage and companion database sidecars.

### Core Tenets
- **Zero Configuration:** Developers never need to write `Dockerfiles`, Kubernetes YAMLs, or CI/CD pipelines to achieve a production-ready deployment.
- **Dynamic Adaptability:** Hardcoded versions are anti-patterns. IdliStack dynamically infers runtimes from source code (e.g., `package.json`, `go.mod`, `.python-version`, `Cargo.toml`).
- **Production-Grade Tooling:** IdliStack utilizes **K3s** (a CNCF-certified lightweight Kubernetes distribution), **Railpack** (Railway's modern BuildKit compiler), and **Helm** (atomic releases, rollbacks, and lifecycle hooks) to ensure exact parity with real-world cloud environments.
- **Zero-Install Client Resilience:** End users do not need to pre-install Railpack. IdliStack's standalone resolver automatically fetches the binary transparently into `~/.idlistack/bin/railpack`, with a built-in multi-stage Dockerfile generator as an offline safety net.

---

## 2. Technology Stack

| Component | Technology | Role & Purpose |
| :--- | :--- | :--- |
| **CLI Framework** | [Cobra](https://github.com/spf13/cobra) (Go) | Command parsing, subcommands (`up`, `down`, `status`, `env`, `init`), flags, and contextual help. |
| **Configuration** | TOML | Parses `idlistack.toml` for optional project-specific overrides. |
| **Primary Build Engine** | [Railpack](https://railpack.com) (v0.39+) | Modern BuildKit-based planner & OCI image compiler by Railway. |
| **Detection Engine** | Railpack + Dynamic Rules | Stack, runtime version, and start command inference with heuristic fallback. |
| **Image Builder** | Docker Engine & BuildKit | Compiles inferred architectures into OCI-compliant images with multi-tier caching. |
| **Cluster Runtime** | K3s (containerd) | Lightweight, single-binary Kubernetes distribution with direct image sideloading. |
| **Orchestrator** | Helm v3 | Scaffolds and manages Kubernetes resources atomically (deployments, rollbacks, hooks). |
| **Management Console** | Embedded HTTP & SSE | Embedded Go web server providing real-time metrics, live log streaming, web shell, and backups. |

---

## 3. The 6-Stage Deployment Pipeline

Every execution of `idlistack up` runs through a deterministic 6-stage lifecycle:

```
┌────────────────────────────────────────────────────────────────────────┐
│                              idlistack up                              │
├────────────┬────────────┬────────────┬────────────┬────────────┬───────┤
│  Stage 1   │  Stage 2   │  Stage 3   │  Stage 4   │  Stage 5   │Stage 6│
│  Validate  │   Detect   │ Save Build │ Build OCI  │  Sideload  │ Helm  │
│  Project   │ Application│   Plan     │ via Railpack│  to K3s   │Deploy │
└────────────┴────────────┴────────────┴────────────┴────────────┴───────┘
```

### 3.1. Stage 1: Workspace Validation & Sizing
- **Config Verification:** Loads `idlistack.toml`. If missing, prompts the user to initialize via `idlistack init` (or generates a preview config when run with `--inspect`).
- **Ignore Engine:** Recursively scans the directory using `.idlistackignore` (and `.gitignore` as fallback). Excludes heavy artifacts (`node_modules`, `.git`, `__pycache__`, `.venv`, `target`, `dist`).
- **Dynamic Size Ceiling:** Enforces `currentSize + 500MB` buffer to protect system resources.

### 3.2. Stage 2: Multi-Layer Detection Engine
The detection hierarchy identifies the stack, runtime version, and start commands:

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
- **Port Detection:** Inspects `.env`, `config.toml`, or defaults based on stack conventions (Node: 3000, Python: 8000, Go: 8080).

### 3.3. Stage 3: Build Plan Serialization
- Serializes the normalized build plan into `.idlistack/buildplan.json`.
- When invoked as `idlistack up --inspect`, outputs formatted JSON and confidence ratings without triggering a build or deployment.

### 3.4. Stage 4: OCI Image Compilation
- **Tag Generation:** Generates a deterministic tag: `idlistack/<project-name>:<unix-timestamp>`.
- **Railpack Compilation:** Triggers `railpack build . --name <imageTag>` utilizing Docker BuildKit caching mounts.
- **Native Fallback:** If Railpack is unavailable, generates an optimized multi-stage Dockerfile (`.idlistack/Dockerfile.generated`) and builds directly via `docker build`.

### 3.5. Stage 5: Cluster Detection & Image Sideloading
To bypass the latency, network cost, and credential management of public Docker registries, IdliStack injects images directly into the local cluster:
- **Cluster Discovery:** Detects K3s, Minikube, Kind, or Docker Desktop via `kubectl config current-context` and node metadata.
- **K3s containerd Import:** Exports the image to `/tmp/idlistack-<time>.tar` and imports via `k3s ctr images import`.
- **Sidecar Pre-loading:** Pulls and sideloads dependency images (`postgres:15-alpine`, `mariadb:10.6`, `redis:7-alpine`, `busybox:1.36`).

### 3.6. Stage 6: Helm Orchestration & Rollout
- **Concurrency Locking:** Acquires `/tmp/idlistack-<app>.lock` via `syscall.Flock`.
- **Chart Generation:** Writes dynamic manifests to `.idlistack/helm/templates/`.
- **Zombie Release Purging:** Identifies and cleans releases stuck in `pending-install`, `pending-upgrade`, or `failed`.
- **Atomic Deployment:** Runs:
  ```bash
  helm upgrade --install <app> .idlistack/helm --namespace idlistack-<app> --create-namespace --wait --timeout 300s --atomic
  ```
- **Crash Diagnostic & Rollback:** If the pod fails readiness probes, IdliStack dumps the last 30 lines of container logs and warning events before executing an atomic rollback.

---

## 4. Subsystems & Key Workflows

### 4.1. Automated Companion Dependencies (Databases & Caches)
IdliStack automatically inspects environment variables and configuration files for database connection strings:
- **PostgreSQL 15:** Injects `Deployment`, `Service`, and persistent 5Gi `PersistentVolumeClaim`.
- **MariaDB 10.6 / MySQL:** Provisions service and automatically mounts any project `database.sql` into `/docker-entrypoint-initdb.d/init.sql` via a Kubernetes `ConfigMap`.
- **Redis 7:** Provisions cache deployment and service.

### 4.2. Traffic Routing & Deterministic NodePorts
- Queries cluster-wide allocated NodePorts to prevent port collisions.
- Computes a deterministic port in `30000–32767` using FNV-1a hashing on the project name.
- Resolves the host Node IP to present the user with a clickable, routable URL (`http://<node-ip>:<node-port>`).

### 4.3. Robust Health Probes & File Permissions
- Employs **TCP Socket Probes** (`startupProbe`, `livenessProbe`, `readinessProbe`) instead of fragile HTTP endpoints.
- Allocates a **150-second startup window** (30 attempts × 5s) to allow slow frameworks or database migrations to complete before liveness probes kick in.
- Injects a `busybox:1.36` `initContainer` to enforce volume ownership (`chmod -R 777` / `chown -R 1000:1000`) for non-root containers.

### 4.4. Interactive Web Dashboard (`idlistack status`)
Running `idlistack status` starts an embedded Go HTTP/SSE server (default port `4200`) providing:
- Real-time CPU and Memory monitoring.
- Interactive Environment Variable & Secret CRUD editor.
- In-browser interactive container terminal shell.
- 1-click on-demand SQL database backup streaming.
- Deployment revision history and 1-click instant Helm rollbacks.
- Live log streaming and replica scaling controls.

---

## 5. Directory Structure & Code Organization

```text
CLI/
├── main.go                          # CLI entry point
├── Makefile                         # Build targets (build, install, clean, test)
├── PRODUCTION_ARCHITECTURE.md       # Technical architecture specification
├── README.md                        # Primary user documentation
├── cmd/                             # Cobra command implementations
│   ├── root.go                      # Global flags and root command context
│   ├── init.go                      # Project initialization
│   ├── up.go                        # The 6-stage deployment pipeline orchestrator
│   ├── down.go                      # Helm uninstall and namespace teardown
│   ├── logs.go                      # Real-time streaming log aggregator
│   ├── status.go                    # Terminal health check & web dashboard launcher
│   └── env.go                       # Kubernetes Secret management
├── internal/                        # Core internal engine logic
│   ├── app/                         # Fast in-memory filesystem scanner
│   ├── buildplan/                   # Standardized build plan schema
│   ├── config/                      # TOML configuration parser
│   ├── dashboard/                   # Embedded Web Dashboard server & static assets
│   ├── detect/                      # Multi-layer detection engine
│   │   ├── detect.go                # Orchestrator & layer priority
│   │   ├── railpack.go              # Railpack v0.39+ resolver, downloader & parser
│   │   ├── rules.go                 # Dynamic framework rules engine
│   │   └── frameworks.json          # Specialized framework signatures
│   ├── k8s/                         # Kubernetes logic
│   │   └── deployer.go              # Helm chart generator, dependencies, and rollout
│   └── ui/                          # Terminal UI components and banners
└── vscode-extension/                # VS Code Marketplace Extension
    ├── package.json                 # Extension manifest (v1.7.0)
    ├── extension.js                 # VS Code extension controller & status bar
    ├── bin/                         # Bundled idlistack executable
    └── idlistack-vscode-1.7.0.vsix  # Packaged extension bundle
```

---

## 6. Production Management & Debugging

When managing an IdliStack application directly via Kubernetes:

```bash
# View all pods, services, and PVCs
kubectl get all,pvc -n idlistack-<app-name>

# View Helm release history
helm history <app-name> -n idlistack-<app-name>

# Roll back manually to a previous revision
helm rollback <app-name> 1 -n idlistack-<app-name>

# View K3s containerd images natively
sudo k3s crictl images

# View running containerd containers
sudo k3s crictl ps
```
