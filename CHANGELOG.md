# Changelog

All notable changes to the **IdliStack CLI** and **IdliStack VS Code Extension** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased] — v1.9.0

### Added
- **Keycloak OIDC Authentication Gate (`idlistack login`, `logout`, `whoami`, `auth`):** Enforces OIDC token verification and Kubernetes RBAC authorization before executing cluster-mutating commands (`init`, `up`, `down`, `status`, `logs`, `env`).
- **K3s RBAC Provisioning (`internal/k8s/rbac.go`):** Automated cluster-wide and namespace-scoped RBAC role binding management for multi-tenant clusters.
- **VS Code Extension v1.9.0:** Added Keycloak Login, Auth Layer Setup, Whoami RBAC status, and Logout commands directly in the VS Code Command Palette and context menus.

---

## [v1.8.1] — 2026-09-28

### Added
- **Interactive Web Dashboard Service CRUD:** Added a Service Management menu on the Admin Dashboard (`idlistack status`) to create, inspect, update, and delete existing and upcoming platform services.

### Fixed
- **K3s Containerd Self-Healing:** Auto-detect Docker node runtime and perform a clean K3s `containerd` restart when image sideloading encounters runtime socket drift.
- **Automated Sudoers & Service Recovery:** Added self-healing K3s systemd service recovery and non-blocking sudoers configuration for `k3s ctr images import`.

---

## [v1.8.0] — 2026-09-28

### Added
- **VS Code Extension v1.8.0:** Enhanced CLI binary resolution priority (`~/.local/bin`, `/usr/local/bin`, bundled extension binary) with dynamic `PATH` injection and one-click integrated terminal access.
- **Deep Heuristic Fallback Detection:** Resilient zero-config stack detection with recursive project scanning and automatic entrypoint inference when standard manifest files are nested or non-standard.

---

## [v1.7.2] — 2026-09-25

### Fixed
- **Self-Healing K3s Installer (`install.sh`):** Automatic repair of broken K3s systemd units, kubeconfig permissions (`~/.kube/config`), and containerd socket readiness checks.
- **VS Code Extension v1.7.2:** Improved error reporting and status bar feedback during long-running BuildKit builds.

---

## [v1.7.0] — 2026-09-20

### Added
- **Multi-Platform Production Installer (`install.sh`):** Automated one-line setup for Docker, K3s, `kubectl`, Helm v3, Railpack, and IdliStack CLI across Ubuntu, Debian, Fedora, RHEL, and Windows WSL2.

### Changed
- **Unified Railpack Detection Engine:** Refactored stack detection to use the native Railpack detection engine (`v0.39+`) with dynamic rule overrides instead of maintaining fragmented manual providers.

---

## [v1.6.0] — 2026-09-15

### Added
- **Optimized PHP & Laravel Detection:** Enhanced stack detection and runtime configuration specifically for PHP/Composer and Laravel applications.

---

## [v1.5.0] — 2026-09-10

### Added
- **VS Code Extension Integration:** Official VS Code extension with status bar controls, context menus, OCI image build orchestration, and stack inspection.

---

## [v1.4.0] — 2026-09-05

### Added
- **Dynamic MySQL / MariaDB Provisioning:** Automatic sidecar database provisioning with `init.sql` bootstrapping and dynamic Ghost CMS environment variable wiring.

### Fixed
- **Dependency Readiness Probes:** Resolved race conditions in database wait commands and improved MySQL TCP/exec readiness probes before application rollout.

---

## [v1.3.0] — 2026-09-01

### Changed
- **Dynamic Rules Engine (`internal/detect/rules.json`):** Replaced hardcoded framework detection logic with a declarative, extensible JSON rules engine.

---

## [v1.2.0] — 2026-08-25

### Added
- **Native Ghost CMS Provider:** Zero-config detection and deployment for Ghost CMS applications with persistent content volume mapping (`PVC`).

---

## [v1.1.0] — 2026-08-18

### Added
- **Railpack Provider Architecture:** Introduced Railpack-based BuildKit image compilation alongside Nixpacks fallback support.

---

## [v1.0.0] — 2026-08-06

### Added
- **Initial Open-Source Release of IdliStack CLI:**
  - Core CLI commands: `idlistack init`, `idlistack up`, `idlistack down`, `idlistack status`, `idlistack logs`, `idlistack env`, and `idlistack version`.
  - 6-stage deployment pipeline: Project validation, stack detection, build plan generation, OCI image build, K3s containerd sideloading, and atomic Helm v3 deployment.
  - Embedded Web Dashboard (`idlistack status`) with live SSE logs, real-time pod metrics, container shell, secret management, and 1-click SQL backups.

[Unreleased]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.8.1...HEAD
[v1.8.1]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.8.0...v1.8.1
[v1.8.0]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.7.2...v1.8.0
[v1.7.2]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.7.0...v1.7.2
[v1.7.0]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.6.0...v1.7.0
[v1.6.0]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.5.0...v1.6.0
[v1.5.0]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.4.0...v1.5.0
[v1.4.0]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.3.0...v1.4.0
[v1.3.0]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.2.0...v1.3.0
[v1.2.0]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.1.0...v1.2.0
[v1.1.0]: https://github.com/saintlionelpraveen/Idlistack-CLI/compare/v1.0.0...v1.1.0
[v1.0.0]: https://github.com/saintlionelpraveen/Idlistack-CLI/releases/tag/v1.0.0
