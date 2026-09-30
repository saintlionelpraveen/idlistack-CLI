# Security Policy

The **IdliStack** team and community take security bugs seriously. Because IdliStack orchestrates container builds, sideloads OCI images into Kubernetes (`containerd`), manages Kubernetes Secrets, and configures Keycloak OIDC & K3s RBAC, we appreciate your efforts to responsibly disclose your findings.

---

## Supported Versions

The following versions of **IdliStack CLI** and the **IdliStack VS Code Extension** are currently supported with security updates:

| Version | Supported | Notes |
| :--- | :--- | :--- |
| `v1.9.x` | :white_check_mark: Yes | Active release branch (Keycloak OIDC + K3s RBAC) |
| `v1.8.x` | :white_check_mark: Yes | Latest stable release |
| `v1.7.x` | :warning: Security fixes only | Upgrade to `v1.8.1+` recommended |
| `< v1.7.0` | :x: No | End of Life (EOL) |

---

## Reporting a Vulnerability

> [!CAUTION]
> **Please do NOT report security vulnerabilities through public GitHub issues, discussions, or pull requests.**

Instead, please report security vulnerabilities privately via one of the following channels:

1. **GitHub Private Vulnerability Reporting:** Use the [Report a vulnerability](https://github.com/saintlionelpraveen/Idlistack-CLI/security/advisories/new) button under the repository's **Security** tab.
2. **Email:** Send a detailed report to **tebidev13@gmail.com** with the subject line `[SECURITY] IdliStack Vulnerability Report: <Brief Summary>`.

### What to Include in Your Report

To help us triage and resolve the issue quickly, please include:
- **Type of issue** (e.g., privilege escalation in `install.sh` / sudoers, container build injection, Kubernetes RBAC bypass, OIDC token handling, dashboard SSE/API exposure, or secret leakage).
- **Affected component(s)** and source file paths (e.g., `internal/k8s/deployer.go`, `internal/auth/`, `internal/dashboard/server.go`, `install.sh`).
- **Affected version(s)** (output of `idlistack version`).
- **Step-by-step reproduction instructions** or a minimal proof-of-concept (PoC).
- **Impact assessment** (what an attacker could achieve and under what prerequisites).

### Response Timeline

- **Initial Acknowledgment:** Within **48 hours** of receiving your report.
- **Triage & Status Update:** Within **5 business days** with severity assessment and remediation plan.
- **Patch & Disclosure:** Coordinated release of a patched version followed by public credit in our `CHANGELOG.md` and GitHub Security Advisories (unless anonymity is requested).

---

## Security Best Practices for Users

- **Protect Kubernetes Secrets:** Never commit `.idlistack/` build state or raw production secrets to version control. Use `idlistack env set KEY=VALUE` to store encrypted secrets in Kubernetes rather than hardcoding them in `idlistack.toml`.
- **Dashboard Binding:** By default, `idlistack status` binds the web dashboard to `127.0.0.1:4200` (localhost only). Do not expose the dashboard port directly to public networks without an authenticated reverse proxy.
- **Verify Installer Integrity:** Inspect `install.sh` before running it with `sudo` in production environments.
