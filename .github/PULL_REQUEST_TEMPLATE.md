## 📋 Summary

<!-- Provide a clear and concise description of what this Pull Request does and why it is needed. -->

## 🔗 Related Issue(s)

<!-- Link the issue(s) this PR resolves, e.g., "Fixes #12" or "Closes #34" -->
- Fixes #

## 🏷️ Type of Change

<!-- Mark the relevant option(s) with an "x" -->
- [ ] 🐛 **Bug Fix** (`fix:`) — Non-breaking change that resolves an issue
- [ ] ✨ **New Feature** (`feat:`) — Non-breaking change that adds functionality
- [ ] 🔍 **Stack Detection** (`feat(detect):` / `fix(detect):`) — New or improved framework/runtime detection rules
- [ ] ☸️ **Kubernetes / Helm / K3s** (`feat(cluster):` / `fix(cluster):`) — Sideloading, Helm chart, PVC, or RBAC update
- [ ] 🖥️ **Web Dashboard** (`feat(dashboard):` / `fix(dashboard):`) — Embedded console UI (`idlistack status`) or SSE/API update
- [ ] 🧩 **VS Code Extension** (`feat(vscode):` / `fix(vscode):`) — Extension commands, status bar, or packaging update
- [ ] 🔐 **Authentication & Security** (`feat(auth):` / `fix(auth):`) — Keycloak OIDC or K3s RBAC changes
- [ ] ⚡ **Refactoring** (`refactor:`) — Code improvement that neither fixes a bug nor adds a feature
- [ ] 📚 **Documentation / Installer** (`docs:` / `chore:`) — Updates to `README.md`, `install.sh`, or GitHub workflows
- [ ] 💥 **Breaking Change** (`feat!:`) — Changes existing CLI flags, `idlistack.toml` schema, or cluster behavior

## 🧩 Component(s) Affected

- [ ] `cmd/` (CLI Commands & Flags)
- [ ] `internal/detect/` (Railpack & JSON Detection Engine)
- [ ] `internal/buildplan/` (Build Plan Schema)
- [ ] `internal/k8s/` (K3s Sideloader, Helm Deployer, Sidecars, RBAC)
- [ ] `internal/dashboard/` (Interactive Web Console)
- [ ] `internal/auth/` (Keycloak OIDC Authentication)
- [ ] `vscode-extension/` (VS Code Extension)
- [ ] `install.sh` / `Makefile` (Installer & Build Tooling)

## 🧪 How Has This Been Tested?

<!-- Describe the tests you ran to verify your changes. -->
- [ ] `make build` compiles cleanly without warnings
- [ ] `make test` (`go test ./... -v`) passes all unit tests
- [ ] Tested `idlistack up --inspect` against a sample project
- [ ] Tested full deployment (`idlistack up` / `idlistack status` / `idlistack down`) on a local K3s cluster

### Terminal Output / Screenshots (Optional)

<!-- Paste relevant CLI output (`idlistack up --inspect`) or Web Dashboard screenshots below -->
```text

```

## ✅ Contributor Checklist

- [ ] My PR title follows [Conventional Commits](https://www.conventionalcommits.org/) format (e.g., `feat(detect): ...` or `fix(cluster): ...`).
- [ ] I have read and followed the [Contributing Guidelines](../CONTRIBUTING.md).
- [ ] I have ensured no secrets, tokens, or `.idlistack/` local state files are included in this commit.
- [ ] I have updated documentation (`README.md`, `CHANGELOG.md`) where applicable.
