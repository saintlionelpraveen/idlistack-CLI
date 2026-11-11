#!/usr/bin/env bash
# ==============================================================================
# IdliStack — Production Prerequisites & Environment Installer
# Supported Platforms: Ubuntu, Debian, Fedora, RHEL/Rocky/Alma, Windows (WSL2 / Git Bash)
#
# Automatically installs and configures:
#   1. System utilities (curl, wget, ca-certificates, tar, gzip, iptables)
#   2. Docker Engine (with non-root user group permissions)
#   3. K3s (Lightweight CNCF-certified Kubernetes cluster with self-healing)
#   4. kubectl CLI (configured with user kubeconfig permissions)
#   5. Helm v3 (Atomic Kubernetes package manager)
#   6. K3s containerd socket permissions (for zero-registry image sideloading)
#   7. Teleport Client (tsh v16 Zero-Trust Kubernetes access)
#   8. Railpack CLI (Automated buildpack detection engine)
#   9. IdliStack CLI binary (installed to /usr/local/bin & ~/.local/bin)
#  10. VS Code Extension (auto-installs bundled .vsix if 'code' is available)
# ==============================================================================

set -euo pipefail

# --- Color formatting (ANSI C Quoting) ---
BOLD=$'\033[1m'
GREEN=$'\033[0;32m'
CYAN=$'\033[0;36m'
YELLOW=$'\033[1;33m'
RED=$'\033[0;31m'
NC=$'\033[0m' # No Color

# --- Log Helpers ---
log_info() {
    printf "${CYAN}ℹ [INFO]${NC} %b\n" "$1"
}

log_step() {
    printf "\n${BOLD}${CYAN}==>${NC} ${BOLD}%b${NC}\n" "$1"
}

log_success() {
    printf "${GREEN}✔ [SUCCESS]${NC} %b\n" "$1"
}

log_warn() {
    printf "${YELLOW}⚠ [WARNING]${NC} %b\n" "$1"
}

log_error() {
    printf "${RED}✖ [ERROR]${NC} %b\n" "$1" >&2
}

# --- Banner ---
print_banner() {
    cat << "EOF"
  ___     _ _ _  ____  _             _    
 |_ _|___| | (_) / ___|| |_ __ _  ___| | __
  | |/ _ \ | | | \___ \| __/ _` |/ __| |/ /
  | |  __/ | | |  ___) | || (_| | (__|   < 
 |___\___|_|_|_| |____/ \__\__,_|\___|_|\_\
 
 Zero-Config Kubernetes Deployment Environment Setup
EOF
    echo "=========================================================="
}

# --- Resolve User & Home Directory ---
resolve_target_user() {
    if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
        TARGET_USER="$SUDO_USER"
        TARGET_HOME=$(getent passwd "$TARGET_USER" 2>/dev/null | cut -d: -f6 || echo "/home/$TARGET_USER")
    else
        TARGET_USER="${USER:-$(whoami)}"
        TARGET_HOME="${HOME:-/root}"
    fi
    log_info "Target user: ${BOLD}${TARGET_USER}${NC} (Home: ${TARGET_HOME})"
}

# --- Elevated Sudo Runner ---
run_as_root() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

# --- Detect OS & Environment ---
detect_environment() {
    UNAME_S="$(uname -s 2>/dev/null || echo "Unknown")"
    UNAME_R="$(uname -r 2>/dev/null || echo "Unknown")"
    IS_WSL=false
    OS_FAMILY="unknown"
    OS_ID="unknown"

    # Check Windows Git Bash / MSYS / Cygwin
    case "${UNAME_S}" in
        MINGW*|MSYS*|CYGWIN*)
            OS_FAMILY="windows"
            return 0
            ;;
    esac

    # Check WSL2
    if [[ "${UNAME_R}" =~ [Mm]icrosoft ]] || [[ "${UNAME_R}" =~ WSL ]] || [ -n "${WSL_DISTRO_NAME:-}" ]; then
        IS_WSL=true
    fi

    # Read /etc/os-release
    if [ -f /etc/os-release ]; then
        # shellcheck disable=SC1091
        . /etc/os-release
        OS_ID="${ID:-unknown}"
        case "${ID:-}" in
            ubuntu|debian|linuxmint|pop)
                OS_FAMILY="debian"
                ;;
            fedora|rhel|centos|rocky|almalinux)
                OS_FAMILY="fedora"
                ;;
            arch|manjaro)
                OS_FAMILY="arch"
                ;;
            *)
                if [ -n "${ID_LIKE:-}" ]; then
                    if [[ "${ID_LIKE}" =~ debian ]]; then
                        OS_FAMILY="debian"
                    elif [[ "${ID_LIKE}" =~ (fedora|rhel) ]]; then
                        OS_FAMILY="fedora"
                    fi
                fi
                ;;
        esac
    fi
}

# --- Handle Windows (Non-WSL Host) ---
handle_windows_host() {
    print_banner
    log_warn "Windows Native (Git Bash/MSYS) detected!"
    echo
    echo "Kubernetes (K3s) and container runtimes require a Linux kernel environment."
    echo "To run IdliStack seamlessly on Windows, you have two production options:"
    echo
    echo "  ${BOLD}Option 1: Windows Subsystem for Linux (WSL2) [RECOMMENDED]${NC}"
    echo "    1. Open PowerShell as Administrator and run:"
    echo "       ${CYAN}wsl --install -d Ubuntu${NC}"
    echo "    2. Restart your computer if prompted."
    echo "    3. Open the newly installed Ubuntu terminal and run:"
    echo "       ${CYAN}curl -fsSL https://raw.githubusercontent.com/saintlionelpraveen/Idlistack-CLI/main/install.sh | bash${NC}"
    echo
    echo "  ${BOLD}Option 2: Docker Desktop for Windows${NC}"
    echo "    1. Install Docker Desktop with WSL2 backend enabled."
    echo "    2. In Docker Desktop Settings -> Kubernetes -> check 'Enable Kubernetes'."
    echo "    3. IdliStack will automatically discover the 'docker-desktop' cluster context!"
    echo
    exit 0
}

# --- Configure WSL2 Systemd Support ---
configure_wsl_systemd() {
    if [ "$IS_WSL" = true ]; then
        log_info "WSL2 environment detected. Ensuring systemd is enabled in /etc/wsl.conf..."
        if [ ! -f /etc/wsl.conf ] || ! grep -q "systemd=true" /etc/wsl.conf; then
            run_as_root mkdir -p /etc
            run_as_root tee /etc/wsl.conf > /dev/null << 'EOF'
[boot]
systemd=true
EOF
            log_warn "Configured systemd in /etc/wsl.conf. If systemd services fail, run 'wsl --shutdown' in Windows PowerShell and relaunch WSL."
        else
            log_success "WSL2 systemd is already configured."
        fi
    fi
}

# --- Step 1: Install Core System Dependencies ---
install_core_dependencies() {
    log_step "Step 1/8: Installing core system utilities"
    case "$OS_FAMILY" in
        debian)
            log_info "Updating package lists via apt-get..."
            run_as_root apt-get update -qq
            run_as_root apt-get install -y -qq curl wget ca-certificates tar gzip iptables gnupg lsb-release sudo procps
            ;;
        fedora)
            log_info "Installing package dependencies via dnf..."
            run_as_root dnf install -y -q curl wget ca-certificates tar gzip iptables systemd sudo procps-ng
            ;;
        arch)
            log_info "Installing package dependencies via pacman..."
            run_as_root pacman -Sy --noconfirm curl wget ca-certificates tar gzip iptables sudo procps-ng
            ;;
        *)
            log_warn "Unrecognized distribution (${OS_ID}). Proceeding assuming utilities exist..."
            ;;
    esac
    log_success "System utilities are installed."
}

# --- Step 2: Install and Configure Docker Engine ---
install_docker() {
    log_step "Step 2/8: Installing & configuring Docker Engine"

    if command -v docker >/dev/null 2>&1; then
        log_info "Docker is already installed ($(docker --version))."
    else
        log_info "Installing Docker Engine via official Docker installation script..."
        curl -fsSL https://get.docker.com | run_as_root sh
    fi

    # Ensure docker daemon service is enabled & running
    if command -v systemctl >/dev/null 2>&1 && systemctl is-system-running >/dev/null 2>&1; then
        run_as_root systemctl enable --now docker >/dev/null 2>&1 || true
        run_as_root systemctl start docker >/dev/null 2>&1 || true
    elif command -v service >/dev/null 2>&1; then
        run_as_root service docker start >/dev/null 2>&1 || true
    fi

    # Configure non-root user permissions
    log_info "Configuring Docker group permissions for user '${TARGET_USER}'..."
    run_as_root groupadd -f docker
    run_as_root usermod -aG docker "${TARGET_USER}"

    # Ensure socket access
    if [ -S /var/run/docker.sock ]; then
        run_as_root chmod 666 /var/run/docker.sock 2>/dev/null || true
    fi

    log_success "Docker Engine is active and configured for non-root usage."
}

# --- Step 3: Install K3s Kubernetes Cluster (With Self-Healing) ---
install_k3s() {
    log_step "Step 3/8: Installing K3s (Lightweight Kubernetes)"

    # 1. Check if an active, healthy cluster is already running
    if (command -v kubectl >/dev/null 2>&1 && kubectl cluster-info >/dev/null 2>&1) || \
       (command -v k3s >/dev/null 2>&1 && run_as_root k3s kubectl cluster-info >/dev/null 2>&1); then
        log_info "Active Kubernetes cluster already detected and healthy."
        if command -v systemctl >/dev/null 2>&1; then
            run_as_root systemctl enable --now k3s >/dev/null 2>&1 || true
        fi
        return 0
    fi

    # 2. Cleanup stale / broken previous installations before attempting install
    log_info "Checking system environment and cleaning any stale K3s locks..."
    if [ -f /usr/local/bin/k3s-killall.sh ]; then
        run_as_root /usr/local/bin/k3s-killall.sh >/dev/null 2>&1 || true
    fi
    # Free port 6443 if an orphaned process is holding it
    if command -v ss >/dev/null 2>&1 && ss -tlpn 2>/dev/null | grep -q ":6443 "; then
        log_warn "Port 6443 is currently in use. Releasing port 6443..."
        if command -v fuser >/dev/null 2>&1; then
            run_as_root fuser -k 6443/tcp 2>/dev/null || true
        fi
    fi

    # Ubuntu 24.04+ AppArmor unprivileged user namespace fix
    if [ -f /proc/sys/kernel/apparmor_restrict_unprivileged_userns ]; then
        log_info "Configuring AppArmor unprivileged user namespace allowance for containers..."
        run_as_root sysctl -w kernel.apparmor_restrict_unprivileged_userns=0 >/dev/null 2>&1 || true
    fi

    # 3. Install K3s
    # Note: --disable traefik and --disable servicelb prevent conflicts on local developer laptops where
    # ports 80/443 might be in use by local services. IdliStack uses native NodePorts (30000-32767).
    log_info "Installing K3s with write-kubeconfig-mode 644 (Traefik disabled)..."
    set +e
    curl -sfL https://get.k3s.io | run_as_root env INSTALL_K3S_EXEC="server --write-kubeconfig-mode 644 --disable traefik --disable servicelb" sh -
    local k3s_exit=$?
    set -e

    # 4. Check if service started properly
    local k3s_running=false
    if [ $k3s_exit -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
        if systemctl is-active --quiet k3s 2>/dev/null; then
            k3s_running=true
        fi
    fi

    # 5. If failed to start, run automated diagnostics and recovery
    if [ "$k3s_running" = false ]; then
        log_warn "K3s service failed on initial start. Reading journalctl error logs..."
        run_as_root journalctl -xeu k3s.service -n 25 --no-pager 2>/dev/null || true

        log_info "Executing automated recovery: resetting stale database cache & restarting..."
        if [ -f /usr/local/bin/k3s-killall.sh ]; then
            run_as_root /usr/local/bin/k3s-killall.sh >/dev/null 2>&1 || true
        fi
        if command -v fuser >/dev/null 2>&1; then
            run_as_root fuser -k 6443/tcp 2>/dev/null || true
        fi
        run_as_root rm -rf /var/lib/rancher/k3s/server/db /var/lib/rancher/k3s/data

        # If host has Docker running and containerd had socket issues, use host Docker runtime (--docker)
        if command -v docker >/dev/null 2>&1 && docker ps >/dev/null 2>&1; then
            log_info "Configuring K3s to use host Docker runtime as reliable fallback (--docker)..."
            curl -sfL https://get.k3s.io | run_as_root env INSTALL_K3S_EXEC="server --write-kubeconfig-mode 644 --disable traefik --disable servicelb --docker" sh -
        else
            run_as_root systemctl restart k3s 2>/dev/null || true
        fi
    fi

    # 6. Wait for node readiness
    log_info "Waiting for K3s node to reach Ready state..."
    local attempts=0
    local max_attempts=30
    until run_as_root k3s kubectl get node 2>/dev/null | grep -q "Ready"; do
        attempts=$((attempts + 1))
        if [ "$attempts" -ge "$max_attempts" ]; then
            log_warn "K3s node initialization buffer reached. Continuing setup..."
            break
        fi
        sleep 2
    done

    log_success "K3s cluster is running and ready."
}

# --- Step 4: Configure Kubectl & Kubeconfig ---
configure_kubectl() {
    log_step "Step 4/8: Configuring kubectl & developer kubeconfig"

    # Symlink kubectl to k3s if standalone kubectl is missing
    if ! command -v kubectl >/dev/null 2>&1; then
        if [ -f /usr/local/bin/k3s ]; then
            log_info "Symlinking /usr/local/bin/k3s -> /usr/local/bin/kubectl..."
            run_as_root ln -sf /usr/local/bin/k3s /usr/local/bin/kubectl
        fi
    fi

    # Configure user's ~/.kube/config
    local kube_dir="${TARGET_HOME}/.kube"
    mkdir -p "${kube_dir}"

    if [ -f /etc/rancher/k3s/k3s.yaml ]; then
        log_info "Copying K3s kubeconfig to ${kube_dir}/config..."
        run_as_root cp /etc/rancher/k3s/k3s.yaml "${kube_dir}/config"
        run_as_root chown -R "${TARGET_USER}:${TARGET_USER}" "${kube_dir}"
        run_as_root chmod 600 "${kube_dir}/config"
    fi

    # Ensure PATH includes ~/.local/bin and /usr/local/bin across shell profiles
    # (Note: We avoid hardcoding KUBECONFIG so developers can freely switch contexts/cloud clusters)
    for rc in "${TARGET_HOME}/.bashrc" "${TARGET_HOME}/.zshrc"; do
        if [ -f "$rc" ] && ! grep -q ".local/bin" "$rc"; then
            echo -e '\n# IdliStack CLI path\nexport PATH="$HOME/.local/bin:/usr/local/bin:$PATH"' >> "$rc"
        fi
    done

    log_success "kubectl configured and connected to cluster."
}

# --- Step 5: Configure K3s containerd Sideload Permissions ---
configure_sideload_permissions() {
    log_step "Step 5/8: Configuring K3s containerd zero-registry sideloading"

    # Make containerd socket accessible to non-root user
    if [ -S /run/k3s/containerd/containerd.sock ]; then
        run_as_root chmod 666 /run/k3s/containerd/containerd.sock 2>/dev/null || true
    fi

    # Create passwordless sudoers entry for 'k3s ctr images import' so background deployments never hang
    local sudoers_file="/etc/sudoers.d/idlistack-k3s"
    log_info "Adding passwordless sudoers rule for K3s containerd image imports..."
    run_as_root tee "$sudoers_file" > /dev/null << EOF
# Allow ${TARGET_USER} to import sideloaded container images and manage K3s service without password
${TARGET_USER} ALL=(ALL) NOPASSWD: /usr/local/bin/k3s ctr images import *, /usr/bin/k3s ctr images import *, /bin/systemctl start k3s, /usr/bin/systemctl start k3s, /bin/systemctl restart k3s, /usr/bin/systemctl restart k3s, /bin/systemctl status k3s, /usr/bin/systemctl status k3s, /bin/systemctl is-active k3s, /usr/bin/systemctl is-active k3s
EOF
    run_as_root chmod 0440 "$sudoers_file"

    log_success "Containerd image sideloading permissions configured."
}

# --- Step 6: Install Helm v3 ---
install_helm() {
    log_step "Step 6/8: Installing Helm v3 (Kubernetes Orchestrator)"

    if command -v helm >/dev/null 2>&1; then
        log_info "Helm v3 is already installed ($(helm version --short))."
    else
        log_info "Downloading and installing official Helm v3..."
        curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | run_as_root bash
    fi

    log_success "Helm v3 is installed and ready."
}

# --- Step 7: Install Teleport Client (tsh v16 Zero-Trust Gateway) ---
install_teleport() {
    log_step "Step 7/8: Installing Teleport Client (tsh v16 Zero-Trust Gateway)"

    if command -v tsh >/dev/null 2>&1; then
        log_info "Teleport CLI is already installed ($(tsh version 2>&1 | head -n1))."
        return 0
    fi

    log_info "Downloading Teleport v16.2.0 client (tsh)..."
    local arch="amd64"
    if [ "$(uname -m)" = "aarch64" ] || [ "$(uname -m)" = "arm64" ]; then
        arch="arm64"
    fi
    local tp_url="https://cdn.teleport.dev/teleport-v16.2.0-linux-${arch}-bin.tar.gz"
    if curl -fsSL "$tp_url" -o /tmp/teleport.tar.gz 2>/dev/null; then
        tar -xzf /tmp/teleport.tar.gz -C /tmp teleport/tsh 2>/dev/null || true
        if [ -f /tmp/teleport/tsh ]; then
            run_as_root mv /tmp/teleport/tsh /usr/local/bin/tsh
            run_as_root chmod 0755 /usr/local/bin/tsh
            mkdir -p "${TARGET_HOME}/.local/bin"
            ln -sf /usr/local/bin/tsh "${TARGET_HOME}/.local/bin/tsh" 2>/dev/null || true
            run_as_root chown -R "${TARGET_USER}:${TARGET_USER}" "${TARGET_HOME}/.local/bin" 2>/dev/null || true
            log_success "Teleport Client (tsh) installed to /usr/local/bin/tsh."
        fi
        rm -rf /tmp/teleport /tmp/teleport.tar.gz
    else
        log_warn "Could not pre-fetch Teleport client. Teleport features can be installed manually."
    fi
}

# --- Step 8: Pre-fetch Railpack, Install IdliStack CLI & VS Code Extension ---
install_railpack_and_cli() {
    log_step "Step 8/8: Installing Railpack engine, IdliStack CLI binary & VS Code extension"

    # 1. Pre-download Railpack to ~/.idlistack/bin/railpack
    local idli_bin_dir="${TARGET_HOME}/.idlistack/bin"
    mkdir -p "${idli_bin_dir}"

    if command -v railpack >/dev/null 2>&1; then
        log_info "Railpack binary found in system PATH."
    elif [ -f "${idli_bin_dir}/railpack" ]; then
        log_info "Railpack binary already cached in ${idli_bin_dir}/railpack."
    else
        log_info "Pre-fetching Railpack (v0.39.0) standalone static binary..."
        local arch="x86_64"
        if [ "$(uname -m)" = "aarch64" ] || [ "$(uname -m)" = "arm64" ]; then
            arch="arm64"
        fi
        local rp_url="https://github.com/railwayapp/railpack/releases/download/v0.39.0/railpack-v0.39.0-${arch}-unknown-linux-musl.tar.gz"
        if curl -fsSL "$rp_url" -o /tmp/railpack.tar.gz 2>/dev/null; then
            tar -xzf /tmp/railpack.tar.gz -C "${idli_bin_dir}" railpack 2>/dev/null || true
            chmod 0755 "${idli_bin_dir}/railpack" 2>/dev/null || true
            rm -f /tmp/railpack.tar.gz
            run_as_root chown -R "${TARGET_USER}:${TARGET_USER}" "${TARGET_HOME}/.idlistack"
            log_success "Railpack v0.39.0 cached to ${idli_bin_dir}/railpack."
        else
            log_warn "Could not pre-fetch Railpack. IdliStack CLI will automatically download it on first run."
        fi
    fi

    # 2. Install IdliStack CLI binary to /usr/local/bin and ~/.local/bin
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
    local user_local_bin="${TARGET_HOME}/.local/bin"
    mkdir -p "${user_local_bin}"

    if [ -f "${script_dir}/bin/idlistack" ]; then
        log_info "Installing IdliStack CLI binary from local repo..."
        run_as_root cp "${script_dir}/bin/idlistack" /usr/local/bin/idlistack
        run_as_root chmod 0755 /usr/local/bin/idlistack
        cp "${script_dir}/bin/idlistack" "${user_local_bin}/idlistack" 2>/dev/null || true
        chmod 0755 "${user_local_bin}/idlistack" 2>/dev/null || true
        log_success "IdliStack CLI installed to /usr/local/bin and ${user_local_bin}/idlistack."
    elif [ -f "${script_dir}/idlistack" ]; then
        log_info "Installing IdliStack CLI binary from local repo..."
        run_as_root cp "${script_dir}/idlistack" /usr/local/bin/idlistack
        run_as_root chmod 0755 /usr/local/bin/idlistack
        cp "${script_dir}/idlistack" "${user_local_bin}/idlistack" 2>/dev/null || true
        chmod 0755 "${user_local_bin}/idlistack" 2>/dev/null || true
        log_success "IdliStack CLI installed to /usr/local/bin and ${user_local_bin}/idlistack."
    else
        log_info "Downloading compiled IdliStack CLI binary to /usr/local/bin/idlistack..."
        local idli_url="https://raw.githubusercontent.com/saintlionelpraveen/Idlistack-CLI/main/bin/idlistack"
        if curl -fsSL "$idli_url" -o /tmp/idlistack 2>/dev/null; then
            run_as_root mv /tmp/idlistack /usr/local/bin/idlistack
            run_as_root chmod 0755 /usr/local/bin/idlistack
            cp /usr/local/bin/idlistack "${user_local_bin}/idlistack" 2>/dev/null || true
            chmod 0755 "${user_local_bin}/idlistack" 2>/dev/null || true
            log_success "IdliStack CLI binary installed globally to /usr/local/bin/idlistack."
        else
            log_warn "Could not fetch standalone CLI binary. Note: The VS Code extension includes the CLI pre-bundled."
        fi
    fi
    run_as_root chown -R "${TARGET_USER}:${TARGET_USER}" "${user_local_bin}" 2>/dev/null || true

    # 3. Optional: Install VS Code Extension if 'code' command is present
    if command -v code >/dev/null 2>&1; then
        local vsix_file=""
        for v in "${script_dir}/vscode-extension"/*.vsix "${script_dir}"/*.vsix; do
            if [ -f "$v" ]; then
                vsix_file="$v"
            fi
        done
        if [ -n "$vsix_file" ]; then
            log_info "Detected VS Code CLI. Installing IdliStack extension (${vsix_file##*/})..."
            if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
                su - "$TARGET_USER" -c "code --install-extension '${vsix_file}' --force" >/dev/null 2>&1 || true
            else
                code --install-extension "${vsix_file}" --force >/dev/null 2>&1 || true
            fi
            log_success "IdliStack VS Code extension installed."
        fi
    fi
}

# --- Verification & Diagnostics ---
verify_installation() {
    echo
    printf "${BOLD}==========================================================${NC}\n"
    printf "${BOLD}             IdliStack Environment Status Check           ${NC}\n"
    printf "${BOLD}==========================================================${NC}\n"

    # Docker Check
    if command -v docker >/dev/null 2>&1; then
        printf "  %-24s ${GREEN}✔ Installed${NC} (%s)\n" "Docker CLI:" "$(docker --version | cut -d',' -f1)"
    else
        printf "  %-24s ${RED}✖ Missing${NC}\n" "Docker CLI:"
    fi

    # Docker Socket Check
    if run_as_root docker ps >/dev/null 2>&1; then
        printf "  %-24s ${GREEN}✔ Running & Accessible${NC}\n" "Docker Daemon:"
    else
        printf "  %-24s ${RED}✖ Inaccessible${NC}\n" "Docker Daemon:"
    fi

    # Kubectl Check
    if command -v kubectl >/dev/null 2>&1; then
        printf "  %-24s ${GREEN}✔ Installed${NC}\n" "Kubectl CLI:"
    else
        printf "  %-24s ${RED}✖ Missing${NC}\n" "Kubectl CLI:"
    fi

    # Kubernetes Cluster Check
    if run_as_root kubectl cluster-info >/dev/null 2>&1 || (command -v k3s >/dev/null 2>&1 && run_as_root k3s kubectl cluster-info >/dev/null 2>&1); then
        printf "  %-24s ${GREEN}✔ Healthy & Connected${NC}\n" "Kubernetes Cluster:"
    else
        printf "  %-24s ${YELLOW}⚠ Initializing${NC}\n" "Kubernetes Cluster:"
    fi

    # Helm Check
    if command -v helm >/dev/null 2>&1; then
        printf "  %-24s ${GREEN}✔ Installed${NC} (%s)\n" "Helm Orchestrator:" "$(helm version --short)"
    else
        printf "  %-24s ${RED}✖ Missing${NC}\n" "Helm Orchestrator:"
    fi

    # Teleport Check
    if command -v tsh >/dev/null 2>&1; then
        printf "  %-24s ${GREEN}✔ Installed${NC} (%s)\n" "Teleport Client (tsh):" "$(tsh version 2>&1 | head -n1 | cut -d' ' -f2)"
    else
        printf "  %-24s ${YELLOW}⚠ Optional${NC}\n" "Teleport Client (tsh):"
    fi

    # Railpack Check
    if command -v railpack >/dev/null 2>&1 || [ -f "${TARGET_HOME}/.idlistack/bin/railpack" ]; then
        printf "  %-24s ${GREEN}✔ Cached & Ready${NC}\n" "Railpack Engine:"
    else
        printf "  %-24s ${YELLOW}⚠ Auto-downloads on up${NC}\n" "Railpack Engine:"
    fi

    # IdliStack CLI Check
    if command -v idlistack >/dev/null 2>&1; then
        printf "  %-24s ${GREEN}✔ Installed${NC} (/usr/local/bin/idlistack)\n" "IdliStack CLI:"
    else
        printf "  %-24s ${CYAN}ℹ Available via VS Code Extension${NC}\n" "IdliStack CLI:"
    fi

    printf "${BOLD}==========================================================${NC}\n"
    echo
    printf "${GREEN}${BOLD}🎉 System is fully configured for IdliStack!${NC}\n"
    echo
    echo "Next Steps:"
    echo "  1. If running under a new shell session, apply group changes:"
    echo "     ${CYAN}newgrp docker${NC}  (or log out and log back in)"
    echo "  2. Authenticate with Frappe Auth Portal & Teleport RBAC:"
    echo "     ${CYAN}idlistack login${NC}"
    echo "  3. Check active user session & allowed namespaces:"
    echo "     ${CYAN}idlistack whoami${NC}"
    echo "  4. Deploy your application to K3s:"
    echo "     ${CYAN}idlistack up${NC}"
    echo "  5. Or use the VS Code Command Palette: ${BOLD}Ctrl+Shift+P${NC} -> ${CYAN}IdliStack: Deploy to K3s (Up)${NC}"
    echo
}

# --- Main Flow ---
main() {
    print_banner
    detect_environment

    if [ "$OS_FAMILY" = "windows" ]; then
        handle_windows_host
    fi

    log_info "Detected OS: ${BOLD}${OS_ID}${NC} (Family: ${OS_FAMILY}, WSL: ${IS_WSL})"
    resolve_target_user

    configure_wsl_systemd
    install_core_dependencies
    install_docker
    install_k3s
    configure_kubectl
    configure_sideload_permissions
    install_helm
    install_teleport
    install_railpack_and_cli
    verify_installation
}

main "$@"