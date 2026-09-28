# IdliStack CLI & VS Code Extension

> **Deploy any application to Kubernetes with zero configuration.**
> Powered by **Go**, **Railpack (BuildKit)**, **Docker**, **Helm**, and **K3s**.

---

## Overview

**IdliStack** brings the developer experience of modern platforms (Railway, Render, Vercel) directly to your local or edge Kubernetes clusters (**K3s**, **Minikube**, **Kind**, or **Docker Desktop**).

With a single command or one click in VS Code, IdliStack:
1. **Detects** your language, framework, runtime version, and dependencies.
2. **Compiles** an optimized OCI container image via **Railpack** using BuildKit caching.
3. **Sideloads** the image directly into your local cluster containerd without public registry delays.
4. **Provisions** database and cache sidecars (**PostgreSQL**, **MariaDB/MySQL**, **Redis**) and persistent storage (PVCs).
5. **Deploys** atomically using native **Helm** charts with TCP health probes and auto-rollback protection.

```bash
# Detect, build, and deploy in one command
idlistack up
```

---

## Key Features

- **Zero Configuration:** No Dockerfile or Kubernetes YAML needed.
- **Powered by Railpack v0.39+:** Builds OCI-compliant images using Railway's modern BuildKit engine.
- **Zero-Install Client Resilience:** The user does not need to install Railpack manually. IdliStack's standalone resolver automatically resolves it, with a built-in multi-stage Dockerfile generator as an offline fallback.
- **Automated Databases & Caches:** Auto-provisions PostgreSQL, MariaDB/MySQL (with `init.sql`), and Redis with persistent volumes.
- **Deterministic Traffic Routing:** Calculates collision-free NodePorts (`30000-32767`) and outputs clickable local URLs (`http://127.0.0.1:<port>`).
- **Interactive Web Dashboard (`idlistack status`):** Embedded web console featuring live log streaming, real-time CPU/memory metrics, container shell, 1-click database SQL backups, and Helm rollbacks.
- **VS Code Extension:** One-click deployment directly from the status bar, command palette, or file context menu.

---

## Technology Stack

| Component | Technology | Role & Purpose |
| :--- | :--- | :--- |
| **CLI Framework** | [Cobra](https://github.com/spf13/cobra) (Go) | Command parsing, flags, and terminal UI |
| **Primary Build Engine** | [Railpack](https://railpack.com) (v0.39+) | BuildKit image compilation with multi-tier caching |
| **Detection Engine** | Railpack + Dynamic Rules | Stack, runtime version, and start command inference |
| **Cluster Runtime** | [K3s](https://k3s.io) (containerd) | Production-grade local Kubernetes distribution |
| **Orchestrator** | [Helm v3](https://helm.sh) | Atomic upgrades, rollbacks, and manifest templating |
| **Terminal UI** | [fatih/color](https://github.com/fatih/color) | Clean, colored, structured CLI output |

---

## Quick Start

### 1. Automated Prerequisites & CLI Installation

You can automatically set up **Docker**, **K3s Kubernetes**, **Kubectl**, **Helm v3**, non-root permissions, and the **IdliStack CLI** with our production-ready installer:

```bash
# Automated install for Ubuntu, Debian, Fedora, RHEL, and Windows (WSL2):
curl -fsSL https://raw.githubusercontent.com/saintlionelpraveen/Idlistack-CLI/main/install.sh | bash
```

Or from a local clone:

```bash
# Clone the repository
git clone https://github.com/saintlionelpraveen/Idlistack-CLI.git
cd Idlistack-CLI

# Run the automated prerequisites & environment installer
./install.sh

# Or build manually with Go:
make install
```

Make sure `~/.local/bin` (or `/usr/local/bin`) is in your `PATH`.

---

### 2. Basic Usage

Navigate to any application directory (Node.js, Python, Go, PHP, Rust, Ruby, etc.):

```bash
# 1. Preview the build plan without building or deploying
idlistack up --inspect

# 2. Build and deploy to Kubernetes
idlistack up

# 3. View live pod logs
idlistack logs

# 4. Check cluster status & open interactive Web Dashboard
idlistack status

# 5. Manage runtime secrets and environment variables
idlistack env set API_KEY="secret-token"
idlistack env list

# 6. Tear down deployment and clean cluster resources
idlistack down
```

---

## The 6-Stage Pipeline

```
[1. Validate Project] ──> [2. Detect Application] ──> [3. Save Build Plan]
                                                              │
[6. Helm K8s Rollout] <── [5. Sideload to Cluster] <── [4. Build OCI Image]
```

1. **Validate Project & Sizing:** Checks `idlistack.toml`, calculates source directory size with `.idlistackignore` / `.gitignore` filtering, and enforces memory ceilings.
2. **Detect Application:** Scans codebase in priority order:
   - *Layer 0:* Existing `Dockerfile` / `docker-compose.yml` (user-provided).
   - *Layer 0.5:* Specialized Dynamic Rules (Ghost CMS, Frappe Bench).
   - *Layer 1:* **Railpack Detection Engine** (Node, Python, Go, Rust, PHP, Java, Ruby, Elixir, .NET, Deno, Bun).
   - *Layer 2:* Deep Heuristic Recursive Fallback (Safety Net).
3. **Save Build Plan:** Normalizes runtime, build commands, and ports into `.idlistack/buildplan.json`.
4. **Build OCI Image:** Compiles image via `railpack build` with BuildKit layer caching (or native multi-stage Dockerfile fallback).
5. **Sideload to Cluster:** Saves and imports images directly into K3s containerd (`k3s ctr images import`) or Minikube/Kind.
6. **Deploy to Kubernetes via Helm:** Scaffolds Helm chart, provisions companion databases/PVCs, configures TCP socket health probes (150s startup window), and executes atomic upgrade with auto-rollback.

---

## Interactive Web Dashboard (`idlistack status`)

Running `idlistack status` starts an embedded Go HTTP/SSE server on `http://127.0.0.1:4200` with:
- **Live Metrics:** Real-time container CPU & Memory graphs.
- **In-Browser Shell:** Execute commands directly in your running pods.
- **Secrets Editor:** Live CRUD for environment variables with zero-downtime rolling restart.
- **1-Click SQL Backup:** Instant streaming download of full database SQL dumps.
- **Deployment History & Rollback:** View past Helm releases and roll back in one click.

---

## VS Code Extension

An official Visual Studio Code extension is located in [`vscode-extension/`](vscode-extension/):

- **Package:** `vscode-extension/idlistack-vscode-1.7.0.vsix`
- **Commands:**
  - `IdliStack: Deploy to K3s (Up)`
  - `IdliStack: Inspect Stack & Plan`
  - `IdliStack: Check Cluster Status`
  - `IdliStack: View Logs`
  - `IdliStack: Destroy (Down)`
- **Persistent Status Bar:** Click **`$(rocket) IdliStack`** in the status bar for instant deployment actions.

### Installing in VS Code

```bash
code --install-extension vscode-extension/idlistack-vscode-1.7.0.vsix
```

---

## Configuration (`idlistack.toml`)

IdliStack is zero-config by default, but supports optional overrides:

```toml
[project]
name = "my-service"

[build]
provider = "node"
runtime = "22"
install_cmd = "pnpm install"
build_cmd = "pnpm build"
start_cmd = "pnpm start"

[deploy]
port = 3000
replicas = 2
health_check_path = "/health"
dependencies = ["postgres", "redis"]

[env]
NODE_ENV = "production"
```

---

## CLI Command Reference

| Command | Flags | Description |
| :--- | :--- | :--- |
| `idlistack init` | `-n, --name` | Initialize project and generate `idlistack.toml` |
| `idlistack up` | `--inspect`, `-d, --detach` | Detect, build, sideload, and deploy to Kubernetes |
| `idlistack status` | `-c, --cli`, `--no-browser`, `-p, --port` | Show cluster status and launch web console |
| `idlistack logs` | `-f, --follow`, `-t, --tail` | Stream live container logs from Kubernetes |
| `idlistack env set` | `KEY=VALUE...` | Set Kubernetes Secrets for the application |
| `idlistack env list` | — | List and decode active environment secrets |
| `idlistack down` | `-f, --force` | Uninstall Helm release and delete namespace |
| `idlistack version`| — | Print IdliStack CLI version |

---
## Local steps to setup

### 1. One-Line Setup (Remote or from GitHub)
curl -fsSL https://raw.githubusercontent.com/saintlionelpraveen/Idlistack-CLI/main/install.sh | bash

### 2. Local Setup (From Cloned Repo)
chmod +x ./install.sh
./install.sh

---
## License

MIT License &bull; Copyright (c) 2026 T4GC / IdliStack Team
