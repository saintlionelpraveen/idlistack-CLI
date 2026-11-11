# IdliStack for Visual Studio Code

<p align="center">
  <strong>Zero-Configuration Kubernetes Deployments with Frappe Auth Portal & Teleport Zero-Trust RBAC</strong>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/version-1.14.0-blue.svg?style=flat-square" alt="Version 1.14.0" />
  <img src="https://img.shields.io/badge/vscode-%3E%3D1.80.0-brightgreen.svg?style=flat-square" alt="VS Code 1.80+" />
  <img src="https://img.shields.io/badge/license-MIT-lightgrey.svg?style=flat-square" alt="License MIT" />
  <img src="https://img.shields.io/badge/kubernetes-K3s%20%7C%20Teleport%20RBAC-orange.svg?style=flat-square" alt="Kubernetes K3s" />
  <img src="https://img.shields.io/badge/auth-Frappe%20Portal-teal.svg?style=flat-square" alt="Frappe Auth" />
</p>

---

**IdliStack** brings the developer experience of platforms like Railway, Vercel, and Render directly to your local or edge Kubernetes clusters (**K3s**, **Minikube**, **Kind**, or **Docker Desktop**).

With a single click or command in VS Code, IdliStack:
1. **Authenticates** the developer via the **Frappe Auth Portal** (`http://localhost:8000/cli_login`).
2. **Authorizes** tenant access and mints an ephemeral, namespace-scoped Kubeconfig via **Teleport Machine-to-Machine (M2M)**.
3. **Inspects** workspace files, inferring programming language, framework, runtime version, and companion dependencies.
4. **Compiles** an optimized OCI container image via **Railpack (BuildKit)**.
5. **Sideloads** the image directly into local K3s containerd without external registry latency.
6. **Deploys** atomically using native **Helm** charts with zero configuration and automated health checks.

---

## Key Features

### 1. Frappe Auth Portal & Zero-Trust Teleport RBAC
- **Web Browser Authentication**: Sign in with your Frappe team credentials (`son@gmail.com`) via a modern React authentication portal.
- **Machine-to-Machine Identity**: Frappe communicates with Teleport Auth Service via a dedicated bot identity (`bot-frappe-deployer`).
- **Namespace-Restricted Ephemeral Kubeconfigs**: Teleport issues short-lived (15-minute) client certificates restricted strictly to your application's namespace (`idlistack-<app>`). Developers cannot view or modify other tenants' workloads.
- **Secure Ephemeral Lifetime**: CLI writes credentials with `0600` permissions and automatically executes cryptographic zero-fill shredding upon termination.

### 2. Zero-Config Language & Framework Detection
- **Supported Stacks**: Node.js (Next.js, Express, Nest, Remix), Python (Django, Flask, FastAPI, Frappe), Go, PHP (Laravel, Symfony, WordPress), Rust, Ruby on Rails, Elixir, .NET, Deno, Bun, and static HTML/Nginx.
- **Smart Runtime Resolution**: Automatically infers exact engine versions from `.nvmrc`, `package.json`, `go.mod`, `Pipfile`, `composer.json`, or `.php-version`.

### 3. Automated Companion Dependencies
- Automatically detects database and cache requirements:
  - **PostgreSQL 15** with persistent storage
  - **MariaDB 10.6 / MySQL** with persistent storage and schema auto-mounting (`database.sql`)
  - **Redis 7** in-memory cache

### 4. Deterministic Traffic Routing & NodePorts
- Resolves collision-free NodePorts (`30000-32767`) using deterministic FNV-1a hashing.
- Outputs clickable local URLs directly in VS Code terminals and notification popups (`http://127.0.0.1:<port>`).

### 5. Production Management Console & Admin Dashboard
- **Embedded Web Console (`idlistack status` on port 4200)**: Real-time CPU/RAM graphs, in-browser container shell, secret CRUD editor, 1-click database SQL dumps, and Helm rollback controls.
- **Admin Dashboard (`idlistack admin` on port 4202)**: Full cluster resource overview, multi-tenant namespace inspection, and IAM status.

---

## Available Commands

Access these commands from the Command Palette (`Ctrl+Shift+P` / `Cmd+Shift+P`), editor context menu, or the persistent status bar item:

| Command | Title | Description |
| :--- | :--- | :--- |
| `idlistack.login` | **IdliStack: Login via Frappe Portal** | Launches browser login portal (`http://localhost:8000`) and saves session |
| `idlistack.admin` | **IdliStack: Open Admin Dashboard** | Launches multi-tenant K3s admin console on port 4202 |
| `idlistack.whoami` | **IdliStack: Check Auth & Teleport Scope** | Displays current user, Frappe URL, and authorized namespaces |
| `idlistack.logout` | **IdliStack: Logout Session** | Revokes active Frappe session and shreds local credentials |
| `idlistack.terminal` | **IdliStack: Open Terminal** | Opens a dedicated terminal with `idlistack` in PATH |
| `idlistack.init` | **IdliStack: Initialize Project** | Generates an `idlistack.toml` configuration template |
| `idlistack.inspect` | **IdliStack: Inspect Stack & Plan** | Previews stack detection, engine version, and build plan |
| `idlistack.up` | **IdliStack: Deploy to K3s (Up)** | Authorizes RBAC, builds OCI image, and deploys via Helm |
| `idlistack.status` | **IdliStack: Check Cluster Status** | Displays pod status and opens the interactive Web Console |
| `idlistack.logs` | **IdliStack: View Logs** | Streams real-time pod logs in the integrated terminal |
| `idlistack.down` | **IdliStack: Destroy (Down)** | Tears down all Kubernetes resources and uninstalls Helm release |
| `idlistack.menu` | **IdliStack: Menu** | Quick-pick action menu accessed from the status bar item |

---

## Persistent Status Bar Integration

The extension adds a live status bar button in the bottom left corner:

- **Authenticated State**: `$(server) IdliStack (son@gmail.com)` — Shows active user and Teleport RBAC status. Clicking opens the action menu.
- **Unauthenticated State**: `$(server) IdliStack (Login Required)` — Alerts developer to sign in via Frappe before deploying.

---

## Quick Start Guide

### Step 1: Install Extension
```bash
code --install-extension vscode-extension/idlistack-vscode-1.14.0.vsix
```

### Step 2: Open Project & Log In
1. Open your project folder in VS Code.
2. Click the status bar item or press `Ctrl+Shift+P` -> `IdliStack: Login via Frappe Portal`.
3. Approve login in your browser (`http://localhost:8000/cli_login`).

### Step 3: Deploy Application
1. Press `Ctrl+Shift+P` -> `IdliStack: Deploy to K3s (Up)`.
2. Watch IdliStack automatically request a scoped Teleport kubeconfig, build the container image, sideload to K3s, and deploy via Helm.
3. Click the live URL shown in the terminal (e.g., `http://127.0.0.1:32249`).

### Step 4: Monitor & Manage
- Press `Ctrl+Shift+P` -> `IdliStack: Check Cluster Status` to launch the live web dashboard on `http://127.0.0.1:4200`.

---

## License

MIT License &bull; Copyright (c) 2026 IdliStack Team
