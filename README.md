# IdliStack CLI & VS Code Extension

[![Latest Release](https://img.shields.io/github/v/release/saintlionelpraveen/Idlistack-CLI?include_prereleases&style=flat-square&logo=github&label=Release)](https://github.com/saintlionelpraveen/Idlistack-CLI/releases)
[![CI Build & Test](https://img.shields.io/github/actions/workflow/status/saintlionelpraveen/Idlistack-CLI/ci.yml?branch=main&style=flat-square&logo=githubactions&label=CI)](https://github.com/saintlionelpraveen/Idlistack-CLI/actions)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-K3s%20%7C%20Helm%20v3-326CE5?style=flat-square&logo=kubernetes&logoColor=white)](https://k3s.io/)
[![Zero-Trust](https://img.shields.io/badge/Security-Teleport%20M2M%20%7C%20Frappe%20Auth-teal?style=flat-square)](https://goteleport.com/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg?style=flat-square)](CONTRIBUTING.md)

> **Deploy any application to Kubernetes with zero configuration and zero-trust RBAC.**
> Powered by **Go**, **Frappe Auth**, **Teleport M2M**, **Railpack (BuildKit)**, **Docker**, **Helm**, and **K3s**.

---

## Overview

**IdliStack** brings the developer experience of modern platforms (Railway, Render, Vercel) directly to your local or edge Kubernetes clusters (**K3s**, **Minikube**, **Kind**, or **Docker Desktop**), backed by enterprise-grade Zero-Trust access control.

With a single command or one click in VS Code, IdliStack:
1. **Authenticates** the developer via the **Frappe Auth Portal** (`http://localhost:8000/cli_login`).
2. **Authorizes** tenant access and mints an ephemeral, namespace-restricted Kubeconfig via **Teleport Machine-to-Machine (M2M)**.
3. **Detects** your language, framework, runtime version, and dependencies.
4. **Compiles** an optimized OCI container image via **Railpack** using BuildKit caching.
5. **Sideloads** the image directly into your local cluster containerd without public registry delays.
6. **Provisions** database and cache sidecars (**PostgreSQL**, **MariaDB/MySQL**, **Redis**) and persistent storage (PVCs).
7. **Deploys** atomically using native **Helm** charts with TCP health probes and auto-rollback protection.

```bash
# Authenticate once via Frappe browser portal
idlistack login

# Detect, build, and deploy to K3s with zero configuration
idlistack up
```

---

## Key Features

- **Zero Configuration:** No Dockerfile or Kubernetes YAML needed.
- **Zero-Trust RBAC & Frappe Control Plane:** Developer identities are managed centrally in Frappe. Every deployment requests an ephemeral, namespace-scoped Kubeconfig via Teleport M2M (`bot-frappe-deployer`).
- **Cryptographic Ephemeral Security:** Ephemeral Kubeconfigs are minted with strict 15-minute lifespans, stored with `0600` file permissions, and automatically sanitized via memory/disk zero-fill shredding upon termination.
- **Powered by Railpack v0.39+:** Builds OCI-compliant images using Railway's modern BuildKit engine with multi-tier caching.
- **Zero-Install Client Resilience:** Railpack resolves automatically transparently; offline multi-stage Dockerfile generators serve as an automatic fallback.
- **Automated Databases & Caches:** Auto-provisions PostgreSQL, MariaDB/MySQL (with `init.sql`), and Redis with persistent volumes.
- **Deterministic Traffic Routing:** Calculates collision-free NodePorts (`30000-32767`) and outputs clickable local URLs (`http://127.0.0.1:<port>`).
- **Interactive Web Dashboard (`idlistack status`):** Embedded web console featuring live log streaming, real-time CPU/memory metrics, container shell, 1-click database SQL backups, and Helm rollbacks on `http://127.0.0.1:4200`.
- **Multi-Tenant Admin Console (`idlistack admin`):** Cluster-wide overview of all tenant namespaces, CPU/RAM allocation, and IAM statuses on `http://127.0.0.1:4202`.
- **VS Code Extension (v1.14.0):** Complete deployment, Frappe login, Teleport scope inspection, and monitoring directly from status bar and context menus.

---

## Technology Stack

| Component | Technology | Role & Purpose |
| :--- | :--- | :--- |
| **CLI Framework** | [Cobra](https://github.com/spf13/cobra) (Go) | Command parsing, flags, and terminal UI |
| **Auth Control Plane** | [Frappe Framework](https://frappeframework.com) | User directory, tenant namespace ownership, and audit trail |
| **Zero-Trust Access** | [Teleport v16](https://goteleport.com) (M2M / `tbot`) | Ephemeral, namespace-restricted X.509 client certificate minting |
| **Primary Build Engine** | [Railpack](https://railpack.com) (v0.39+) | BuildKit image compilation with multi-tier caching |
| **Detection Engine** | Railpack + Dynamic Rules | Stack, runtime version, and start command inference |
| **Cluster Runtime** | [K3s](https://k3s.io) (containerd) | Production-grade lightweight Kubernetes distribution |
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

### 2. Developer Workflow

Navigate to any application directory (Node.js, Python, Go, PHP, Rust, Ruby, etc.):

```bash
# 1. Log in via Frappe browser authentication portal
idlistack login

# 2. Verify your session and Teleport RBAC namespace scope
idlistack whoami

# 3. Preview the build plan without building or deploying
idlistack up --inspect

# 4. Build and deploy to Kubernetes (automatically requests Teleport Kubeconfig)
idlistack up

# 5. View live container logs
idlistack logs

# 6. Check cluster status & open interactive Web Dashboard
idlistack status

# 7. Manage runtime secrets and environment variables
idlistack env set API_KEY="secret-token"
idlistack env list

# 8. Open multi-tenant Admin Console
idlistack admin

# 9. Tear down deployment and clean cluster resources
idlistack down

# 10. Log out session
idlistack logout
```

---

## The 6-Stage Deployment Pipeline

```
[1. Validate Project & Teleport Auth] ──> [2. Detect Application] ──> [3. Save Build Plan]
                                                                                │
[6. Helm K8s Rollout] <── [5. Sideload to Cluster] <── [4. Build OCI Image] ────┘
```

1. **Validate Project & Teleport RBAC:** Verifies `idlistack.toml`, checks filesystem size with ignore filters, verifies the Frappe session, and requests an ephemeral, namespace-scoped Kubeconfig from Teleport via Frappe.
2. **Detect Application:** Scans codebase in priority order:
   - *Layer 0:* Existing `Dockerfile` / `docker-compose.yml` (user-provided).
   - *Layer 0.5:* Specialized Dynamic Rules (Ghost CMS, Frappe Bench).
   - *Layer 1:* **Railpack Detection Engine** (Node, Python, Go, Rust, PHP, Java, Ruby, Elixir, .NET, Deno, Bun).
   - *Layer 2:* Deep Heuristic Recursive Fallback (Safety Net).
3. **Save Build Plan & Project State:** Normalizes runtime, build commands, and ports into a single **`.idlistack.json`** file in the project root.
4. **Build OCI Image:** Compiles image via `railpack build` with BuildKit layer caching (or native multi-stage Dockerfile fallback in temp storage).
5. **Sideload to Cluster:** Saves and imports images directly into K3s containerd (`k3s ctr images import`) bypassing public registries.
6. **Deploy to Kubernetes via Helm:** Dynamically scaffolds transient Helm manifests in `/tmp`, provisions companion databases/PVCs, configures TCP socket health probes (150s startup window), and executes atomic upgrade with auto-rollback under the scoped Teleport credentials before cleaning up chart files.

---

## Teleport Zero-Trust Security Architecture

```
┌──────────────┐     1. Browser Login      ┌────────────────────────────────┐
│  Developer   │ ────────────────────────> │ Frappe Control Plane (port 8000)│
│  CLI / VSCode│ <──────────────────────── │ User Directory & RBAC Policies │
└──────┬───────┘   Saved Session Token     └───────────────┬────────────────┘
       │                                                   │
       │ 2. Request Scoped Kubeconfig                      │ 3. tbot Identity Request
       │    (app_name, namespace)                          │    (bot-frappe-deployer)
       ▼                                                   ▼
┌────────────────────────────────────────┐         ┌────────────────────────────────┐
│ Ephemeral Kubeconfig (/tmp/idlistack) │ <─────── │ Teleport Auth Service          │
│ - Strict 15-minute expiration          │         │ - Issues X.509 client certs    │
│ - Scoped to namespace: idlistack-<app> │         │ - Restricts to k3s-cluster     │
│ - Zero-fill shredding on exit          │         └────────────────────────────────┘
└──────┬─────────────────────────────────┘
       │
       │ 4. Helm Upgrade & Sideload
       ▼
┌────────────────────────────────────────┐
│ K3s Cluster (containerd + NodePort)    │
│ - App: idlistack-<app> (Port: 30000+)  │
│ - DB: PostgreSQL / MariaDB / Redis     │
└────────────────────────────────────────┘
```

---

## Interactive Web Dashboards

### Developer Dashboard (`idlistack status`)
Running `idlistack status` starts an embedded Go HTTP/SSE server on `http://127.0.0.1:4200` with:
- **Live Metrics:** Real-time container CPU & Memory graphs.
- **In-Browser Shell:** Execute commands directly in your running pods.
- **Secrets Editor:** Live CRUD for environment variables with zero-downtime rolling restart.
- **1-Click SQL Backup:** Instant streaming download of full database SQL dumps.
- **Deployment History & Rollback:** View past Helm releases and roll back in one click.

### Admin Dashboard (`idlistack admin`)
Running `idlistack admin` launches the multi-tenant administration console on `http://127.0.0.1:4202`:
- **Cluster Capacity:** Aggregate node CPU, Memory, and Pod utilization.
- **Tenant Isolation:** View all active namespaces and running deployments across developers.
- **Operations:** Scale replicas, restart deployments, or view tenant logs centrally.

---

## VS Code Extension (v1.14.0)

An official Visual Studio Code extension is located in [`vscode-extension/`](vscode-extension/):

- **Package:** `vscode-extension/idlistack-vscode-1.14.0.vsix`
- **Commands:**
  - `IdliStack: Login via Frappe Portal`
  - `IdliStack: Open Admin Dashboard`
  - `IdliStack: Check Auth & Teleport Scope (Whoami)`
  - `IdliStack: Logout Session`
  - `IdliStack: Deploy to K3s (Up)`
  - `IdliStack: Inspect Stack & Plan`
  - `IdliStack: Check Cluster Status`
  - `IdliStack: View Logs`
  - `IdliStack: Destroy (Down)`
- **Persistent Status Bar:** Click **`$(server) IdliStack (<user>)`** in the status bar for instant authentication and deployment actions.

### Installing in VS Code

```bash
code --install-extension vscode-extension/idlistack-vscode-1.14.0.vsix
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
| `idlistack login` | `--cli`, `-u`, `-p`, `--frappe-url`, `--port` | Authenticate with Frappe portal and bind Teleport session |
| `idlistack whoami` | — | Inspect authenticated user and allowed Teleport namespaces |
| `idlistack logout` | — | Revoke Frappe session and delete local credentials |
| `idlistack init` | `-n, --name` | Initialize project and generate `idlistack.toml` |
| `idlistack up` | `--inspect`, `-d, --detach` | Detect, build, sideload, and deploy to Kubernetes |
| `idlistack status` | `-c, --cli`, `--no-browser`, `-p, --port` | Show pod status and launch developer web console (4200) |
| `idlistack admin` | `-p, --port`, `--no-browser` | Launch multi-tenant cluster Admin Dashboard (4202) |
| `idlistack logs` | `-f, --follow`, `-t, --tail` | Stream live container logs from Kubernetes |
| `idlistack env set` | `KEY=VALUE...` | Set Kubernetes Secrets for the application |
| `idlistack env list` | — | List and decode active environment secrets |
| `idlistack down` | `-f, --force` | Uninstall Helm release and delete namespace |
| `idlistack version` | — | Print IdliStack CLI version |

---

## Teleport Management Commands

When administering the Teleport Auth service directly:

```bash
# 1. Check Teleport Cluster Status
tctl status

# 2. List Connected Kubernetes Clusters
tctl kube ls

# 3. List Active Machine-to-Machine Bots
tctl bots ls

# 4. List Teleport Users & Roles
tctl users ls
```

---

## Contributing & Open Source Community

We welcome contributions of all kinds — from adding new stack detection rules to improving the K3s/Helm deployer, Web Dashboard, or VS Code extension!

- **Contributing Guide:** Read [CONTRIBUTING.md](CONTRIBUTING.md) for local setup, architecture overview, branch conventions, and PR workflow.
- **Raise an Issue:**
  - [🐛 Report a Bug](https://github.com/saintlionelpraveen/Idlistack-CLI/issues/new?template=bug_report.yml)
  - [✨ Request a Feature or New Stack](https://github.com/saintlionelpraveen/Idlistack-CLI/issues/new?template=feature_request.yml)
  - [📚 Documentation / Installer Feedback](https://github.com/saintlionelpraveen/Idlistack-CLI/issues/new?template=documentation.yml)
- **Pull Request Format:** All PRs automatically use our structured [Pull Request Template](.github/PULL_REQUEST_TEMPLATE.md).
- **Release History:** See [CHANGELOG.md](CHANGELOG.md) and [GitHub Releases](https://github.com/saintlionelpraveen/Idlistack-CLI/releases).
- **Security Policy:** See [SECURITY.md](SECURITY.md) for responsible vulnerability disclosure.
- **Code of Conduct:** See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

---

## License

Licensed under the [MIT License](LICENSE) &bull; Copyright (c) 2026 T4GC / IdliStack Team
