# IdliStack Cloud K3s Setup Guide

This guide provides the complete, production-ready walkthrough for provisioning a **Cloud K3s** cluster and connecting the **IdliStack CLI** to it. 

You can continue developing and testing locally on your machine today, and use this guide whenever you are ready to transition your deployments to the cloud.

---

## Architecture: Local K3s vs. Cloud K3s

| Pipeline Component | Local K3s (Current) | Cloud K3s |
| :--- | :--- | :--- |
| **Cluster API Server** | `https://127.0.0.1:6443` | `https://<CLOUD_PUBLIC_IP>:6443` (TLS SAN configured) |
| **Authentication / IAM** | Keycloak on `http://127.0.0.1:30080` | Keycloak on `http://<CLOUD_PUBLIC_IP>:30080` |
| **RBAC Token Scoping** | In-Cluster ServiceAccount JWT | In-Cluster ServiceAccount JWT (exact same) |
| **Image Sideloading** | Local containerd socket | SSH streaming or in-cluster registry |
| **Helm Rollouts** | `helm upgrade --install` (via `:6443`) | `helm upgrade --install` (via `:6443`) |
| **Application Traffic** | `http://127.0.0.1:<NodePort>` | `http://<CLOUD_PUBLIC_IP>:<NodePort>` (or Domain Ingress) |

---

## Step 1: Choose and Provision Your Cloud Server

Because K3s is extremely lightweight, you do not need expensive managed Kubernetes services (EKS/GKE). Any standard Linux Virtual Private Server (VPS) running **Ubuntu 22.04 or 24.04** is ideal.

| Cloud Provider | Plan | Cost | Description |
| :--- | :--- | :--- | :--- |
| **Oracle Cloud** *(Best Free)* | Ampere A1 (Up to 4 OCPU, 24GB RAM) | **$0 (Always Free)** | 100% Free Forever. Runs K3s, Keycloak, databases, and multiple apps. |
| **Hetzner Cloud** *(Cheapest Paid)* | CX22 (2 vCPU, 4GB RAM) | **€3.79 / month (~$4)** | High performance, European and US locations. |
| **DigitalOcean** | Basic Droplet (1-2 vCPU, 2-4GB RAM) | **$6 - $12 / month** | Instant setup, simple dashboard. |
| **AWS (Amazon Web Services)** | `t4g.small` or `t3.small` (2GB RAM) | **Free Tier (12 months)** | Standard AWS cloud infrastructure. |

Once your cloud instance is running, note its **Public IPv4 Address** (e.g., `203.0.113.50`).

---

## Step 2: Configure Cloud Firewall & Security Groups

In your cloud provider's firewall or Security Group settings, allow inbound traffic on the following ports:

| Port | Protocol | Purpose | Source |
| :--- | :--- | :--- | :--- |
| **22** | TCP | SSH Server Access | Your IP (or `0.0.0.0/0`) |
| **6443** | TCP | K3s Kubernetes API Server | Your IP (or `0.0.0.0/0`) |
| **30080** | TCP | Keycloak Authentication Portal | `0.0.0.0/0` |
| **30000–32767** | TCP | IdliStack NodePort Application Routes | `0.0.0.0/0` |
| **80 / 443** | TCP | HTTP/HTTPS (if using Ingress/Traefik) | `0.0.0.0/0` |

---

## Step 3: Install K3s on Your Cloud Server

1. Connect to your cloud server via SSH:
   ```bash
   ssh root@<YOUR_CLOUD_PUBLIC_IP>
   ```

2. Run the production K3s installer (replace `<YOUR_CLOUD_PUBLIC_IP>` with your server's actual public IP):
   ```bash
   curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server --tls-san <YOUR_CLOUD_PUBLIC_IP> --write-kubeconfig-mode 644 --disable traefik --disable servicelb" sh -
   ```

   > **Note on `--tls-san`:** This parameter ensures K3s generates TLS certificates that trust connections sent to your cloud server's public IP address, preventing certificate validation errors when connecting from your laptop.

3. Verify that the K3s node is running and in `Ready` state:
   ```bash
   k3s kubectl get nodes -o wide
   ```

---

## Step 4: Connect Your Laptop to Cloud K3s

Run these commands in your **local developer machine's terminal**:

1. Download the K3s configuration file from your cloud server into `~/.kube/cloud-k3s.yaml`:
   ```bash
   scp root@<YOUR_CLOUD_PUBLIC_IP>:/etc/rancher/k3s/k3s.yaml ~/.kube/cloud-k3s.yaml
   ```

2. Replace the local loopback address (`127.0.0.1`) with your Cloud Public IP:
   ```bash
   sed -i 's/127.0.0.1/<YOUR_CLOUD_PUBLIC_IP>/g' ~/.kube/cloud-k3s.yaml
   ```

3. Set your active Kubernetes context to the cloud cluster:
   ```bash
   export KUBECONFIG=~/.kube/cloud-k3s.yaml
   ```
   *(To make this permanent, add `export KUBECONFIG=~/.kube/cloud-k3s.yaml` to your `~/.bashrc` or `~/.zshrc`).*

4. Verify connectivity from your laptop:
   ```bash
   kubectl get nodes -o wide
   ```
   You should see your remote Cloud K3s node listed directly in your laptop terminal.

---

## Step 5: Deploy IdliStack Auth to Cloud K3s

Now that your local `kubectl` is pointed at Cloud K3s:

1. Deploy the Keycloak identity layer, OIDC client, and RBAC controllers:
   ```bash
   idlistack auth setup
   ```
   This deploys Keycloak into the `idlistack-auth` namespace on your cloud server and configures the NodePort service (`:30080`).

2. Authenticate with Keycloak:
   ```bash
   idlistack login
   ```
   Open the browser auth portal, sign in, or create a namespace-scoped developer account.

---

## Step 6: Deploy Applications to Cloud K3s

Navigate to any application directory (e.g., PHP, Node.js, Python, Go):

```bash
cd ~/path/to/my-app
idlistack up
```

IdliStack will:
1. Detect your language runtime and dependencies.
2. Compile the OCI container image.
3. Deliver the image into Cloud K3s.
4. Scaffold and apply the Helm chart atomically with health checks.
5. Output the live public URL:
   ```text
   URL: http://<YOUR_CLOUD_PUBLIC_IP>:<NODE_PORT>
   ```

---

## Production CI/CD: Automated Git-Push Deployments

To automate deployments so that every `git push` to `main` updates your cloud cluster without manual terminal commands, add the following GitHub Actions workflow:

`.github/workflows/deploy.yml`:
```yaml
name: Continuous Deployment to Cloud K3s

on:
  push:
    branches: [ main ]

jobs:
  deploy:
    name: Build & Deploy
    runs-on: ubuntu-latest
    steps:
      - name: Checkout Code
        uses: actions/checkout@v4

      - name: Configure K3s Kubeconfig
        run: |
          mkdir -p ~/.kube
          echo "${{ secrets.CLOUD_K3S_KUBECONFIG }}" > ~/.kube/config
          chmod 600 ~/.kube/config

      - name: Install IdliStack CLI
        run: |
          curl -fsSL https://raw.githubusercontent.com/saintlionelpraveen/Idlistack-CLI/main/install.sh | bash

      - name: Deploy to Cloud K3s
        run: |
          idlistack up --detach
```

### GitHub Repository Secret Setup:
1. In your GitHub repository, navigate to **Settings** -> **Secrets and variables** -> **Actions**.
2. Add a new repository secret named `CLOUD_K3S_KUBECONFIG`.
3. Paste the contents of your `~/.kube/cloud-k3s.yaml` file into the secret.

---

## Production Security Best Practices

1. **Firewall Lockdown:** Once your laptop or CI runner IP is static, restrict port `6443` (Kubernetes API Server) to only your known IP addresses.
2. **Ingress with SSL/TLS:** For production domains (`https://app.example.com`), install `cert-manager` and configure an Ingress rule pointing port 80/443 to your application's Service.
3. **Keycloak HTTPS:** In production, route Keycloak traffic through Traefik or an NGINX Ingress controller with a valid Let's Encrypt SSL certificate (`https://auth.example.com`).
