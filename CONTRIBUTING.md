# Contributing to IdliStack CLI

First off, thank you for considering contributing to **IdliStack**! :rocket:
IdliStack is an open-source zero-configuration Kubernetes deployment platform powered by **Go**, **Railpack (BuildKit)**, **Helm v3**, and **K3s**. Every bug report, stack detection improvement, documentation fix, and feature contribution helps make Kubernetes accessible to developers everywhere.

By participating in this project, you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md).

---

## Table of Contents

1. [Ways to Contribute](#1-ways-to-contribute)
2. [How to Raise an Issue](#2-how-to-raise-an-issue)
3. [Development Environment Setup](#3-development-environment-setup)
4. [Repository Architecture](#4-repository-architecture)
5. [Adding or Improving Stack Detection Rules](#5-adding-or-improving-stack-detection-rules)
6. [Pull Request Workflow & PR Format](#6-pull-request-workflow--pr-format)
7. [Commit Message & Tagging Conventions](#7-commit-message--tagging-conventions)

---

## 1. Ways to Contribute

- :bug: **Report Bugs:** Found an application stack that fails detection, an OCI build failure, or a K3s/Helm rollout edge case? Let us know!
- :sparkles: **Add Framework / Stack Detection:** Expand our declarative rules in `internal/detect/rules.json` and `internal/detect/frameworks.json`.
- :computer: **Enhance the Web Dashboard (`idlistack status`):** Improve live metrics, terminal UX, service CRUD, or SQL backup workflows in `internal/dashboard/`.
- :puzzle_piece: **Improve the VS Code Extension:** Add new IDE commands or UI polish in `vscode-extension/`.
- :books: **Improve Documentation & Installer:** Help refine `README.md`, `PRODUCTION_ARCHITECTURE.md`, or `install.sh` for new Linux distributions.

---

## 2. How to Raise an Issue

We use structured **GitHub Issue Forms** located in [`.github/ISSUE_TEMPLATE/`](.github/ISSUE_TEMPLATE/) to make reporting issues fast and reproducible.

### Step-by-Step Guide to Raising an Issue

1. **Search Existing Issues:** Check the [Issues tab](https://github.com/saintlionelpraveen/Idlistack-CLI/issues) to see if the bug or feature has already been reported.
2. **Choose the Right Issue Template:**
   - **:bug: Bug Report (`bug_report.yml`):** When `idlistack up`, stack detection, container sideloading, Helm deployment, or the VS Code extension behaves unexpectedly.
   - **:sparkles: Feature Request / New Stack Support (`feature_request.yml`):** When proposing a new CLI flag, framework detector, sidecar database, or dashboard capability.
   - **:books: Documentation / Installer Issue (`documentation.yml`):** When reporting unclear docs or `install.sh` platform issues.
3. **Collect Diagnostic Information (for Bug Reports):**
   Before submitting a bug report, run the following commands in your project directory and paste the output into the issue form:
   ```bash
   # 1. Check IdliStack CLI and Kubernetes versions
   idlistack version
   kubectl version --client
   helm version --short

   # 2. Inspect how IdliStack detects your project (safe, does not deploy)
   idlistack up --inspect

   # 3. Run with --verbose to capture detailed stage output
   idlistack up --verbose
   ```
4. **Never Paste Sensitive Secrets:** Redact any real API keys, passwords, or OIDC tokens from `.idlistack/buildplan.json` or terminal logs before posting.
5. **Security Vulnerabilities:** Do **not** open a public issue for security bugs. Follow our [Security Policy](SECURITY.md) instead.

---

## 3. Development Environment Setup

### Prerequisites

- **Go:** `1.24+` (or `1.26+` as specified in [`go.mod`](go.mod))
- **Docker:** Running daemon with BuildKit support
- **Kubernetes (Local):** [K3s](https://k3s.io) (recommended), Minikube, or Kind
- **Helm:** `v3.12+`
- **Node.js (Optional):** `v18+` if packaging the VS Code extension (`vscode-extension/`)

### Clone & Build Locally

```bash
# 1. Fork and clone the repository
git clone https://github.com/<your-username>/Idlistack-CLI.git
cd Idlistack-CLI

# 2. Build the CLI binary into ./bin/idlistack
make build

# 3. Install to ~/.local/bin/idlistack
make install

# 4. Run the test suite
make test

# 5. Quick build and run with arguments
make dev ARGS="up --inspect"
```

---

## 4. Repository Architecture

```text
Idlistack-CLI/
├── main.go                       # Entrypoint invoking cmd.Execute()
├── Makefile                      # Build, install, clean, and test targets
├── install.sh                    # Multi-distro automated prerequisites & CLI installer
├── cmd/                          # Cobra CLI command definitions
│   ├── root.go                   # Root command, global flags, Keycloak auth gate
│   ├── auth.go                   # login, logout, whoami, and auth setup commands
│   ├── init.go                   # idlistack init (scaffolds idlistack.toml)
│   ├── up.go                     # idlistack up (6-stage build & deploy pipeline)
│   ├── down.go                   # idlistack down (Helm uninstall & namespace cleanup)
│   ├── status.go                 # idlistack status (CLI status & Web Dashboard launcher)
│   ├── logs.go                   # idlistack logs (live pod log streaming)
│   └── env.go                    # idlistack env set/list (Kubernetes Secret management)
├── internal/
│   ├── app/                      # Core 6-stage pipeline orchestration
│   ├── auth/                     # Keycloak OIDC client & token verification
│   ├── buildplan/                # Normalized buildplan.json schema & serialization
│   ├── config/                   # idlistack.toml parser and validation
│   ├── dashboard/                # Embedded Go HTTP/SSE Web Console (port 4200)
│   ├── detect/                   # Multi-layer stack detection (Railpack + JSON rules)
│   ├── k8s/                      # K3s containerd sideloader, Helm deployer, RBAC
│   └── ui/                       # Terminal spinner, tables, and colored output
└── vscode-extension/             # Official IdliStack VS Code Extension
```

---

## 5. Adding or Improving Stack Detection Rules

IdliStack detects frameworks without requiring a `Dockerfile`:
1. Check [`internal/detect/rules.json`](internal/detect/rules.json) and [`internal/detect/frameworks.json`](internal/detect/frameworks.json) to add or refine framework heuristics.
2. Add unit tests in [`internal/detect/detect_test.go`](internal/detect/detect_test.go) or [`internal/detect/detect_orchestrator_test.go`](internal/detect/detect_orchestrator_test.go).
3. Verify all detection tests pass:
   ```bash
   go test ./internal/detect/... -v
   ```

---

## 6. Pull Request Workflow & PR Format

### Branch Naming Convention

Create a descriptive branch from `main` based on the type of change:
- `feat/<short-description>` — New features (e.g., `feat/keycloak-rbac`, `feat/mongodb-sidecar`)
- `fix/<short-description>` — Bug fixes (e.g., `fix/k3s-containerd-socket`)
- `docs/<short-description>` — Documentation updates (e.g., `docs/contributing-guide`)
- `refactor/<short-description>` — Code refactoring without behavior changes
- `chore/<short-description>` — CI, build tooling, or dependency updates

### Pull Request Format

When you open a Pull Request, our template ([`.github/PULL_REQUEST_TEMPLATE.md`](.github/PULL_REQUEST_TEMPLATE.md)) will automatically populate. Ensure your PR includes:

1. **PR Title in Conventional Commit Format:**
   ```text
   <type>(<scope>): <concise summary of the change>
   ```
   *Examples:*
   - `feat(detect): add automatic FastAPI & Uvicorn entrypoint inference`
   - `fix(cluster): auto-detect docker node runtime and restart k3s containerd`
   - `feat(vscode): add Keycloak OIDC login command to status bar`
2. **Summary & Motivation:** Explain *what* changed and *why* it is needed.
3. **Linked Issue:** Reference the issue using `Fixes #<issue-number>` or `Closes #<issue-number>`.
4. **Component Checklist:** Mark which subsystem (`cmd`, `internal/detect`, `internal/k8s`, `internal/dashboard`, `vscode-extension`, `install.sh`) was touched.
5. **Verification Output:** Paste the output of `make test`, `make build`, or a screenshot/log of `idlistack up`.

---

## 7. Commit Message & Tagging Conventions

### Conventional Commits

We follow the [Conventional Commits](https://www.conventionalcommits.org/) specification so changelogs and semantic version releases can be generated cleanly:

| Prefix | Meaning | Version Bump |
| :--- | :--- | :--- |
| `feat:` / `feat(scope):` | A new feature for the CLI, detector, dashboard, or VS Code extension | `MINOR` (`v1.x.0`) |
| `fix:` / `fix(scope):` | A bug fix in detection, building, K3s sideloading, or Helm rollout | `PATCH` (`v1.x.y`) |
| `refactor:` | Code change that neither fixes a bug nor adds a feature | `PATCH` |
| `docs:` | Documentation-only changes | — |
| `chore:` / `ci:` | Build scripts, GitHub Actions, or dependency maintenance | — |
| `feat!:` / `BREAKING CHANGE:` | Incompatible CLI flag or `idlistack.toml` schema change | `MAJOR` (`vX.0.0`) |

### Creating a Release Tag (Maintainers)

IdliStack uses annotated Semantic Version Git tags (`vMAJOR.MINOR.PATCH`). Pushing a tag automatically triggers the [GitHub Release workflow](.github/workflows/release.yml):

```bash
# Create an annotated release tag
git tag -a v1.9.0 -m "Release v1.9.0: Keycloak OIDC Auth Gate & K3s RBAC"

# Push the tag to GitHub to trigger automated release binaries
git push origin v1.9.0
```

---

Thank you for helping build **IdliStack**! :heart:
