# KubeBolt — Distribution Methods Specification

> **Version:** 1.0  
> **Status:** Draft  
> **Last Updated:** April 2026  
> **Existing:** Helm Chart (Phase 1.5 — Done)  
> **This document covers:** Single Binary, Homebrew, Docker Single-Container, Kubernetes Operator, kubectl Plugin (krew)

---

## 1. Single Binary

### 1.1 Overview

A single statically-linked binary that embeds the Go API server and the React frontend assets. The user downloads it, runs it, and opens a browser. No Docker, no Node.js, no dependencies.

**Goal:** The fastest possible path from zero to full cluster visibility. One command to install, one command to run.

### 1.2 User Experience

```bash
# Install (auto-detects OS and architecture)
curl -fsSL https://get.kubebolt.dev | sh

# Or download manually
# macOS (Apple Silicon)
curl -LO https://github.com/clm-cloud-solutions/kubebolt/releases/latest/download/kubebolt-darwin-arm64
chmod +x kubebolt-darwin-arm64 && mv kubebolt-darwin-arm64 /usr/local/bin/kubebolt

# Run
kubebolt

# With options
kubebolt --kubeconfig ~/.kube/config --port 3000 --context production-eks
```

Output:
```
⚡ KubeBolt v1.6.0
✓ Loaded kubeconfig: /home/user/.kube/config
✓ Found 3 contexts: docker-desktop, staging-gke, production-eks
✓ Connected to docker-desktop (24 pods, 3 nodes)
✓ Metrics Server detected
✓ Insights engine started (12 rules)
→ Dashboard ready at http://localhost:3000
```

### 1.3 Technical Implementation

#### Frontend Embedding

Use Go's `embed.FS` to bundle the production-built frontend into the binary:

```go
// cmd/server/main.go
package main

import "embed"

//go:embed web/dist/*
var frontendFS embed.FS
```

The build process:
1. `cd apps/web && npm run build` → produces `dist/` directory
2. Copy `dist/` to `cmd/server/web/dist/`
3. `go build -o kubebolt ./cmd/server/` → single binary with embedded frontend

#### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--kubeconfig` | `~/.kube/config` | Path to kubeconfig file |
| `--context` | current-context | Kubernetes context to connect to |
| `--port` | `3000` | HTTP server port |
| `--host` | `0.0.0.0` | Bind address |
| `--metrics-interval` | `30s` | Metrics polling interval |
| `--insights-interval` | `60s` | Insights evaluation interval |
| `--open` | `true` | Auto-open browser on start |
| `--version` | — | Print version and exit |

#### HTTP Server

The single binary serves both the API and the frontend from one HTTP server:

```
:3000/                  → embedded frontend (index.html, JS, CSS)
:3000/api/v1/*          → REST API (existing Chi router)
:3000/ws                → WebSocket (existing hub)
```

The frontend is served with proper cache headers (immutable for hashed assets, no-cache for index.html). The API routes take precedence over the catch-all frontend route (SPA fallback to index.html for client-side routing).

#### Build Matrix

| OS | Architecture | Binary Name | Notes |
|----|-------------|-------------|-------|
| Linux | amd64 | `kubebolt-linux-amd64` | Primary target |
| Linux | arm64 | `kubebolt-linux-arm64` | Raspberry Pi, ARM servers |
| macOS | arm64 | `kubebolt-darwin-arm64` | Apple Silicon (primary Mac target) |
| macOS | amd64 | `kubebolt-darwin-amd64` | Intel Macs |
| Windows | amd64 | `kubebolt-windows-amd64.exe` | Windows 10/11 |

Build with:
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.version=${VERSION}" -o kubebolt-linux-amd64 ./cmd/server/
```

Binary size target: < 30MB (compressed < 10MB).

#### Install Script (`get.kubebolt.dev`)

A shell script hosted at `get.kubebolt.dev` that:
1. Detects OS and architecture (`uname -s`, `uname -m`)
2. Downloads the correct binary from GitHub Releases
3. Verifies checksum (SHA256)
4. Moves to `/usr/local/bin/kubebolt` (or `$HOME/.local/bin` without sudo)
5. Prints success message with run command

```bash
#!/bin/sh
set -e
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in x86_64) ARCH="amd64";; aarch64|arm64) ARCH="arm64";; esac
VERSION=$(curl -sI https://github.com/clm-cloud-solutions/kubebolt/releases/latest | grep -i location | sed 's/.*tag\///' | tr -d '\r')
URL="https://github.com/clm-cloud-solutions/kubebolt/releases/download/${VERSION}/kubebolt-${OS}-${ARCH}"
echo "⚡ Installing KubeBolt ${VERSION} (${OS}/${ARCH})..."
curl -fsSL "$URL" -o /tmp/kubebolt && chmod +x /tmp/kubebolt
sudo mv /tmp/kubebolt /usr/local/bin/kubebolt 2>/dev/null || mv /tmp/kubebolt "$HOME/.local/bin/kubebolt"
echo "✓ KubeBolt installed. Run: kubebolt"
```

### 1.4 GitHub Release Automation

Extend the existing release workflow (`.github/workflows/release.yml`) to:

1. Trigger on `v*` tags
2. Build frontend: `npm ci && npm run build`
3. Copy `dist/` to `cmd/server/web/dist/`
4. Cross-compile Go binary for all 5 platform/arch combinations
5. Generate SHA256 checksums
6. Create GitHub Release with binaries + checksums attached
7. Update `get.kubebolt.dev` to point to latest release

### 1.5 Priority

**High** — This is the lowest-friction distribution method. Should be implemented first among the new methods.

---

## 2. Homebrew

### 2.1 Overview

Distribute KubeBolt via a Homebrew tap for macOS and Linux. Users install and update with familiar `brew` commands.

**Depends on:** Single Binary (Section 1) — Homebrew downloads the pre-built binary from GitHub Releases.

### 2.2 User Experience

```bash
# Install
brew install clm-cloud-solutions/tap/kubebolt

# Run
kubebolt

# Update
brew upgrade kubebolt

# Uninstall
brew uninstall kubebolt
```

### 2.3 Technical Implementation

#### Tap Repository

Create a new GitHub repository: `github.com/clm-cloud-solutions/homebrew-tap`

This repository contains a single formula file:

```ruby
# Formula/kubebolt.rb
class Kubebolt < Formula
  desc "Instant Kubernetes monitoring — full cluster visibility in under 2 minutes"
  homepage "https://github.com/clm-cloud-solutions/kubebolt"
  version "1.6.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://github.com/clm-cloud-solutions/kubebolt/releases/download/v#{version}/kubebolt-darwin-arm64"
      sha256 "PLACEHOLDER_SHA256"
    end
    on_intel do
      url "https://github.com/clm-cloud-solutions/kubebolt/releases/download/v#{version}/kubebolt-darwin-amd64"
      sha256 "PLACEHOLDER_SHA256"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/clm-cloud-solutions/kubebolt/releases/download/v#{version}/kubebolt-linux-arm64"
      sha256 "PLACEHOLDER_SHA256"
    end
    on_intel do
      url "https://github.com/clm-cloud-solutions/kubebolt/releases/download/v#{version}/kubebolt-linux-amd64"
      sha256 "PLACEHOLDER_SHA256"
    end
  end

  def install
    binary_name = "kubebolt-#{OS.mac? ? "darwin" : "linux"}-#{Hardware::CPU.arm? ? "arm64" : "amd64"}"
    bin.install Dir["kubebolt-*"].first => "kubebolt"
  end

  test do
    assert_match "KubeBolt", shell_output("#{bin}/kubebolt --version")
  end
end
```

#### Release Automation

Add a step to the release workflow that:
1. Computes SHA256 of each binary
2. Uses `mislav/bump-homebrew-formula-action` or a custom script to update the formula in `homebrew-tap`
3. Commits and pushes to the tap repository

```yaml
# In .github/workflows/release.yml
- name: Update Homebrew formula
  env:
    HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
  run: |
    # Clone tap repo
    git clone https://x-access-token:${HOMEBREW_TAP_TOKEN}@github.com/clm-cloud-solutions/homebrew-tap.git
    cd homebrew-tap
    # Update version and SHA256 in Formula/kubebolt.rb
    # ... sed replacements for version and sha256 ...
    git commit -am "kubebolt ${VERSION}" && git push
```

### 2.4 Priority

**Medium** — Easy to implement once the binary exists. The tap is just a formula file pointing to GitHub Release assets.

---

## 3. Docker Single-Container

### 3.1 Overview

A single Docker image that runs both the Go API server and serves the React frontend via the embedded assets (same as the binary). The user runs one `docker run` command and opens their browser.

**Difference from existing Docker Compose:** The current setup uses two containers (api + nginx). This is a single self-contained image.

### 3.2 User Experience

```bash
# Basic usage
docker run -p 3000:3000 -v ~/.kube:/root/.kube:ro ghcr.io/clm-cloud-solutions/kubebolt

# EKS (needs AWS credentials)
docker run -p 3000:3000 \
  -v ~/.kube:/root/.kube:ro \
  -v ~/.aws:/root/.aws:ro \
  -e AWS_PROFILE=my-profile \
  ghcr.io/clm-cloud-solutions/kubebolt

# With custom port and context
docker run -p 8080:8080 \
  -v ~/.kube:/root/.kube:ro \
  -e KUBEBOLT_PORT=8080 \
  -e KUBEBOLT_CONTEXT=production-eks \
  ghcr.io/clm-cloud-solutions/kubebolt

# Background mode
docker run -d --name kubebolt -p 3000:3000 -v ~/.kube:/root/.kube:ro ghcr.io/clm-cloud-solutions/kubebolt
```

### 3.3 Technical Implementation

#### Dockerfile

```dockerfile
# ── Stage 1: Build frontend ──
FROM node:20-alpine AS frontend
WORKDIR /build
COPY apps/web/package*.json ./
RUN npm ci
COPY apps/web/ ./
RUN npm run build

# ── Stage 2: Build Go binary ──
FROM golang:1.22-alpine AS backend
WORKDIR /build
COPY go.work go.work.sum ./
COPY apps/api/ apps/api/
COPY packages/ packages/
# Embed frontend
COPY --from=frontend /build/dist apps/api/cmd/server/web/dist/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /kubebolt ./apps/api/cmd/server/

# ── Stage 3: Runtime ──
FROM alpine:3.19
RUN apk add --no-cache ca-certificates aws-cli
COPY --from=backend /kubebolt /usr/local/bin/kubebolt
EXPOSE 3000
ENTRYPOINT ["kubebolt"]
CMD ["--host", "0.0.0.0", "--port", "3000"]
```

**Key points:**
- Multi-stage build keeps final image small (< 50MB)
- `aws-cli` included for EKS token generation
- `ca-certificates` for TLS connections to cloud K8s APIs
- Uses the same binary with embedded frontend as Section 1
- No nginx needed — Go serves everything

#### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `KUBEBOLT_PORT` | `3000` | HTTP server port |
| `KUBEBOLT_HOST` | `0.0.0.0` | Bind address |
| `KUBEBOLT_KUBECONFIG` | `/root/.kube/config` | Kubeconfig path |
| `KUBEBOLT_CONTEXT` | current-context | K8s context |
| `KUBEBOLT_METRICS_INTERVAL` | `30s` | Metrics polling interval |
| `AWS_PROFILE` | — | AWS profile for EKS |

#### Multi-Arch Build

Extend the existing CI to build and push multi-arch images:

```yaml
# In .github/workflows/release.yml
- name: Build and push single-container image
  run: |
    docker buildx build \
      --platform linux/amd64,linux/arm64 \
      --tag ghcr.io/clm-cloud-solutions/kubebolt:${VERSION} \
      --tag ghcr.io/clm-cloud-solutions/kubebolt:latest \
      --push \
      -f deploy/Dockerfile.single .
```

#### Docker Desktop Kubernetes Caveat

When running KubeBolt in Docker against Docker Desktop's K8s, the kubeconfig's `127.0.0.1:6443` doesn't work from inside the container. Two options:

1. **Existing helper script:** `./deploy/docker-kubeconfig.sh` rewrites to `kubernetes.docker.internal`
2. **Auto-detection in binary:** On startup, if the API server address is `127.0.0.1` or `localhost` and we're running inside a container (detect via `/proc/1/cgroup` or `/.dockerenv`), automatically try `kubernetes.docker.internal` instead.

Option 2 is better UX and should be implemented in the binary.

### 3.4 Priority

**High** — Very easy to implement since it reuses the same binary from Section 1 with a thin Dockerfile wrapper. The existing multi-arch pipeline can be adapted.

---

## 4. Kubernetes Operator

### 4.1 Overview

A Kubernetes Operator that manages KubeBolt instances declaratively via a Custom Resource Definition (CRD). The operator handles installation, upgrades, configuration changes, and self-healing.

**Difference from Helm:** Helm installs and forgets — the chart is a template engine. The operator continuously reconciles the desired state, can handle upgrades automatically, and can perform more complex lifecycle management.

### 4.2 User Experience

```bash
# Install the operator
kubectl apply -f https://get.kubebolt.dev/operator.yaml

# Create a KubeBolt instance
cat <<EOF | kubectl apply -f -
apiVersion: kubebolt.io/v1alpha1
kind: KubeBolt
metadata:
  name: kubebolt
  namespace: kubebolt-system
spec:
  version: "1.6.0"               # optional, defaults to latest
  replicas: 1
  resources:
    requests:
      memory: "64Mi"
      cpu: "50m"
    limits:
      memory: "256Mi"
      cpu: "500m"
  ingress:
    enabled: true
    host: kubebolt.example.com
    className: nginx
    tls: true
  serviceAccount:
    create: true
    clusterRole: kubebolt-reader  # auto-created by operator
  copilot:
    enabled: true
    provider: anthropic
    apiKeySecret: kubebolt-copilot-key  # reference to a K8s Secret
EOF

# Check status
kubectl get kubebolt
# NAME       STATUS    VERSION   AGE
# kubebolt   Running   1.6.0     5m

# Upgrade
kubectl patch kubebolt kubebolt --type merge -p '{"spec":{"version":"1.7.0"}}'

# Delete
kubectl delete kubebolt kubebolt
```

### 4.3 Technical Implementation

#### CRD Specification

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: kubebolts.kubebolt.io
spec:
  group: kubebolt.io
  names:
    kind: KubeBolt
    listKind: KubeBoltList
    plural: kubebolts
    singular: kubebolt
    shortNames:
      - kb
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      subresources:
        status: {}
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                version:
                  type: string
                  description: "KubeBolt version to deploy. Defaults to latest."
                replicas:
                  type: integer
                  default: 1
                  minimum: 1
                  maximum: 3
                resources:
                  type: object
                  properties:
                    requests:
                      type: object
                      properties:
                        memory: { type: string, default: "64Mi" }
                        cpu: { type: string, default: "50m" }
                    limits:
                      type: object
                      properties:
                        memory: { type: string, default: "256Mi" }
                        cpu: { type: string, default: "500m" }
                ingress:
                  type: object
                  properties:
                    enabled: { type: boolean, default: false }
                    host: { type: string }
                    className: { type: string }
                    tls: { type: boolean, default: false }
                    annotations:
                      type: object
                      additionalProperties: { type: string }
                serviceAccount:
                  type: object
                  properties:
                    create: { type: boolean, default: true }
                    name: { type: string }
                    annotations:
                      type: object
                      additionalProperties: { type: string }
                copilot:
                  type: object
                  properties:
                    enabled: { type: boolean, default: false }
                    provider: { type: string, enum: [anthropic, openai, custom] }
                    model: { type: string }
                    apiKeySecret: { type: string }
                    baseUrl: { type: string }
            status:
              type: object
              properties:
                phase:
                  type: string
                  enum: [Pending, Running, Upgrading, Failed]
                version:
                  type: string
                conditions:
                  type: array
                  items:
                    type: object
                    properties:
                      type: { type: string }
                      status: { type: string }
                      lastTransitionTime: { type: string, format: date-time }
                      reason: { type: string }
                      message: { type: string }
      additionalPrinterColumns:
        - name: Status
          type: string
          jsonPath: .status.phase
        - name: Version
          type: string
          jsonPath: .status.version
        - name: Age
          type: date
          jsonPath: .metadata.creationTimestamp
```

#### Operator Controller (Go)

Built with Kubebuilder. The controller reconciles `KubeBolt` CRs and manages:

**Owned resources (created and managed by the operator):**
1. **Deployment** — KubeBolt API + embedded frontend (single-container image)
2. **Service** — ClusterIP service exposing port 3000
3. **ServiceAccount** — With configurable annotations (for IRSA, Workload Identity)
4. **ClusterRole** — Read permissions for all 22 resource types KubeBolt monitors
5. **ClusterRoleBinding** — Binds the ClusterRole to the ServiceAccount
6. **Ingress** (optional) — When `spec.ingress.enabled: true`
7. **Secret** (optional) — Copilot API key reference

**Reconciliation logic:**

```
On KubeBolt CR create/update:
  1. Ensure namespace exists
  2. Create/update ServiceAccount (with annotations for cloud identity)
  3. Create/update ClusterRole + ClusterRoleBinding
  4. Create/update Deployment
     - Image: ghcr.io/clm-cloud-solutions/kubebolt:{spec.version}
     - Resources: from spec.resources
     - Env: KUBEBOLT_PORT, copilot config from Secret
  5. Create/update Service
  6. If ingress.enabled: create/update Ingress
  7. Update status: phase=Running, version={spec.version}

On version change (spec.version updated):
  1. Update status: phase=Upgrading
  2. Update Deployment image tag
  3. Wait for rollout completion
  4. Update status: phase=Running, version={new version}

On KubeBolt CR delete:
  1. ClusterRole and ClusterRoleBinding cleaned up (ownerRef doesn't work cross-namespace)
  2. All namespaced resources cleaned up via ownerReferences (automatic)
```

#### Operator Deployment Manifest

The `operator.yaml` file that users apply contains:
1. Namespace: `kubebolt-system`
2. CRD definition
3. Operator Deployment (single replica)
4. Operator ServiceAccount + RBAC (permissions to manage Deployments, Services, Ingresses, ClusterRoles, etc.)

```yaml
# operator.yaml (simplified structure)
apiVersion: v1
kind: Namespace
metadata:
  name: kubebolt-system
---
# CRD (from above)
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kubebolt-operator
  namespace: kubebolt-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubebolt-operator
rules:
  - apiGroups: ["kubebolt.io"]
    resources: ["kubebolts", "kubebolts/status"]
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["services", "serviceaccounts"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["clusterroles", "clusterrolebindings"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kubebolt-operator
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kubebolt-operator
subjects:
  - kind: ServiceAccount
    name: kubebolt-operator
    namespace: kubebolt-system
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kubebolt-operator
  namespace: kubebolt-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kubebolt-operator
  template:
    metadata:
      labels:
        app: kubebolt-operator
    spec:
      serviceAccountName: kubebolt-operator
      containers:
        - name: operator
          image: ghcr.io/clm-cloud-solutions/kubebolt-operator:latest
          resources:
            requests: { memory: "64Mi", cpu: "50m" }
            limits: { memory: "128Mi", cpu: "200m" }
```

#### Project Structure

```
packages/operator/
├── cmd/operator/
│   └── main.go
├── api/v1alpha1/
│   ├── kubebolt_types.go
│   └── zz_generated.deepcopy.go
├── internal/controller/
│   ├── kubebolt_controller.go
│   ├── deployment.go          # Deployment reconciliation
│   ├── service.go             # Service reconciliation
│   ├── rbac.go                # ClusterRole + ClusterRoleBinding
│   ├── ingress.go             # Ingress reconciliation
│   └── status.go              # Status updates
├── config/
│   ├── crd/                   # Generated CRD YAML
│   ├── rbac/                  # Operator RBAC
│   └── manager/               # Operator Deployment
├── Dockerfile
├── go.mod
└── Makefile
```

Add to `go.work`:
```
use ./packages/operator
```

### 4.4 Priority

**Medium-Low** — More complex to implement than the binary/Homebrew/Docker methods. Best suited for Phase 1.6 or later. Provides the most value for enterprise users who manage everything declaratively.

---

## 5. kubectl Plugin (krew)

### 5.1 Overview

A kubectl plugin that runs KubeBolt as a subcommand: `kubectl kubebolt`. Installed via krew (the kubectl plugin manager). Under the hood, it's the same binary from Section 1, registered as a kubectl plugin.

### 5.2 User Experience

```bash
# Install krew if not already installed
# (see https://krew.sigs.k8s.io/docs/user-guide/setup/install/)

# Install KubeBolt plugin
kubectl krew install kubebolt

# Run
kubectl kubebolt

# With specific context
kubectl kubebolt --context production-eks

# With custom port
kubectl kubebolt --port 8080

# Uses the same kubeconfig and context as kubectl itself
kubectl config use-context staging
kubectl kubebolt  # connects to staging
```

### 5.3 Technical Implementation

#### How kubectl Plugins Work

kubectl discovers plugins by looking for executables named `kubectl-<name>` in `$PATH`. When the user runs `kubectl kubebolt`, kubectl finds and executes `kubectl-kubebolt`.

The KubeBolt binary just needs to be renamed to `kubectl-kubebolt`. No code changes needed — the same binary from Section 1 works as a kubectl plugin.

#### Krew Manifest

Create a krew plugin manifest for submission to the [krew-index](https://github.com/kubernetes-sigs/krew-index):

```yaml
# plugins/kubebolt.yaml
apiVersion: krew.googlecontainertools.github.com/v1alpha2
kind: Plugin
metadata:
  name: kubebolt
spec:
  version: v1.6.0
  homepage: https://github.com/clm-cloud-solutions/kubebolt
  shortDescription: "Instant Kubernetes monitoring — full cluster visibility in under 2 minutes"
  description: |
    KubeBolt gives you full cluster visibility in under 2 minutes.
    No agents, no Prometheus, no configuration. 23 resource views,
    interactive cluster topology map, 12-rule insights engine,
    and built-in AI copilot.
    
    Run `kubectl kubebolt` to start the dashboard on http://localhost:3000.
  caveats: |
    This plugin starts a local web server. Open http://localhost:3000 in your browser.
    Press Ctrl+C to stop.
  platforms:
    - selector:
        matchLabels:
          os: darwin
          arch: arm64
      uri: https://github.com/clm-cloud-solutions/kubebolt/releases/download/v1.6.0/kubebolt-darwin-arm64.tar.gz
      sha256: PLACEHOLDER
      bin: kubectl-kubebolt
    - selector:
        matchLabels:
          os: darwin
          arch: amd64
      uri: https://github.com/clm-cloud-solutions/kubebolt/releases/download/v1.6.0/kubebolt-darwin-amd64.tar.gz
      sha256: PLACEHOLDER
      bin: kubectl-kubebolt
    - selector:
        matchLabels:
          os: linux
          arch: arm64
      uri: https://github.com/clm-cloud-solutions/kubebolt/releases/download/v1.6.0/kubebolt-linux-arm64.tar.gz
      sha256: PLACEHOLDER
      bin: kubectl-kubebolt
    - selector:
        matchLabels:
          os: linux
          arch: amd64
      uri: https://github.com/clm-cloud-solutions/kubebolt/releases/download/v1.6.0/kubebolt-linux-amd64.tar.gz
      sha256: PLACEHOLDER
      bin: kubectl-kubebolt
    - selector:
        matchLabels:
          os: windows
          arch: amd64
      uri: https://github.com/clm-cloud-solutions/kubebolt/releases/download/v1.6.0/kubebolt-windows-amd64.zip
      sha256: PLACEHOLDER
      bin: kubectl-kubebolt.exe
```

#### Release Packaging

Krew expects tar.gz archives (or zip for Windows). The release workflow needs to:

1. Build the binary as `kubectl-kubebolt` (instead of `kubebolt`)
2. Package in tar.gz: `tar czf kubebolt-linux-amd64.tar.gz kubectl-kubebolt`
3. Upload to GitHub Release alongside the regular binaries
4. Update the krew manifest with new version and SHA256 values

```yaml
# In release workflow
- name: Package kubectl plugin
  run: |
    for PLATFORM in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
      cp kubebolt-${PLATFORM} kubectl-kubebolt
      tar czf kubebolt-${PLATFORM}.tar.gz kubectl-kubebolt
      rm kubectl-kubebolt
    done
    # Windows
    cp kubebolt-windows-amd64.exe kubectl-kubebolt.exe
    zip kubebolt-windows-amd64.zip kubectl-kubebolt.exe
```

#### kubectl Context Integration

When run as a kubectl plugin, the binary should automatically respect `KUBECONFIG` env var and the current kubectl context. This already works if the binary uses `clientcmd.NewDefaultClientConfigLoadingRules()` — which it does.

Additional behaviors for kubectl plugin mode:
- Detect that it's running as a plugin (check `argv[0]` contains `kubectl-`)
- Default `--open` to `true` (auto-open browser)
- Print the URL prominently since the user launched from terminal

#### Krew Index Submission

To get listed in the official krew index:
1. Fork `kubernetes-sigs/krew-index`
2. Add `plugins/kubebolt.yaml`
3. Submit a PR
4. Krew maintainers review and merge

While waiting for official listing, users can install from a custom index:

```bash
kubectl krew index add clm https://github.com/clm-cloud-solutions/krew-index.git
kubectl krew install clm/kubebolt
```

### 5.4 Priority

**Medium** — Low implementation effort (the binary is already the plugin, just needs renaming and krew manifest). Highly relevant for the K8s audience. Can be done in parallel with Homebrew.

---

## 6. Implementation Order & Dependencies

```
Phase 1 (foundation):
  [1] Single Binary ← everything else depends on this
      ├── embed.FS for frontend
      ├── CLI flags
      ├── Install script (get.kubebolt.dev)
      └── Release workflow with cross-compilation

Phase 2 (quick wins, use the binary):
  [2] Docker Single-Container ← thin Dockerfile around the binary
  [3] Homebrew ← formula pointing to binary releases
  [4] kubectl Plugin (krew) ← rename binary + manifest

Phase 3 (dedicated effort):
  [5] Kubernetes Operator ← separate controller project with Kubebuilder
```

### Effort Estimates

| Method | Effort | Dependencies | Priority |
|--------|--------|-------------|----------|
| Single Binary | 2-3 days | Frontend build + embed.FS + release workflow | **P0** |
| Docker Single-Container | 0.5 day | Single Binary | **P1** |
| Homebrew | 0.5 day | Single Binary + tap repo | **P1** |
| kubectl Plugin (krew) | 0.5 day | Single Binary | **P1** |
| Kubernetes Operator | 5-7 days | Single-container image | **P2** |

### Shared Infrastructure

All methods share:
- The same Go binary with embedded frontend (from Section 1)
- The same GitHub Release workflow
- The same container image (for Docker and Operator)
- The same `get.kubebolt.dev` domain for the install script and operator manifest

---

## 7. Security Considerations

### Binary Distribution
- SHA256 checksums published alongside binaries
- GitHub Release signing with Sigstore cosign (future)
- Install script verifies checksums before moving to PATH

### Container Images
- Signed with cosign and published to GHCR
- SBOM (Software Bill of Materials) attached to image
- Non-root container user (operator and single-container)
- Read-only filesystem where possible

### Operator
- Operator runs with minimal RBAC (only manages KubeBolt-owned resources)
- KubeBolt instance runs with read-only ClusterRole (no write, no exec, no secrets)
- Copilot API keys stored in Kubernetes Secrets, never in CRD spec

---

*End of specification.*
