# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

KubeBolt is an instant Kubernetes monitoring platform — full cluster visibility in under 2 minutes with zero configuration. Go backend + React frontend monorepo. Supports multi-cluster switching and Gateway API resources.

## Build & Run Commands

### Backend (Go)
```bash
cd apps/api && go run cmd/server/main.go --kubeconfig ~/.kube/config  # Run dev server (port 8080)
cd apps/api && go build ./...                                          # Build
cd apps/api && go test ./...                                           # Run tests
cd apps/api && go test ./internal/insights/...                         # Run single package tests
```

### Frontend (React)
```bash
cd apps/web && npm install    # Install dependencies
cd apps/web && npm run dev    # Vite dev server (port 5173)
cd apps/web && npm run build  # Production build (TypeScript check + Vite)
```

### Docker Compose (full stack)
```bash
# Remote clusters (EKS, GKE, AKS) — works directly:
kubectl config use-context my-cluster
cd deploy && docker compose up -d

# Docker Desktop K8s — needs kubeconfig rewrite (127.0.0.1 → kubernetes.docker.internal):
kubectl config use-context docker-desktop
./deploy/docker-kubeconfig.sh   # generates /tmp/docker-kubeconfig
cd deploy && docker compose up -d

# Rebuild after code changes:
docker compose -f deploy/docker-compose.yml up -d --build
```
Frontend on http://localhost:3000 (nginx proxies /api and /ws to backend).
EKS clusters require `~/.aws` mounted (already in compose) with an active AWS session.

## Architecture

### Go Workspace Monorepo

Uses `go.work` with three modules:
- `apps/api` — main backend server
- `packages/agent` — Phase 2 lightweight node agent (stub)
- `packages/shared` — shared Go utilities

### Backend (`apps/api`)

Entry point: `cmd/server/main.go` (flags: `--kubeconfig`, `--port`)

Key packages under `internal/`:
- **cluster/manager.go** — Multi-cluster manager: reads all kubeconfig contexts, handles cluster switching, manages connector/collector/engine lifecycle per cluster
- **cluster/connector.go** — Kubernetes client-go shared informers for all resource types + dynamic client for Gateway API (Gateways, HTTPRoutes)
- **cluster/graph.go** — In-memory topology graph with debounced rebuild (2s)
- **cluster/relationships.go** — Edge detection: ownerRefs, selectors, Gateway parentRefs, volumes
- **metrics/collector.go** — Polls Metrics Server API (`metrics.k8s.io/v1beta1`) every 30s with synchronous initial poll. In-memory cache, no DB.
- **insights/engine.go** — 12 rule-based insight evaluations (crash-loop, OOM, CPU throttle, memory pressure, etc.)
- **websocket/hub.go** — WebSocket connection management (4096 buffer, silent drops when no clients)
- **api/router.go** — Chi router: `/api/v1/clusters`, `/cluster/overview`, `/resources/:type`, `/topology`, `/insights`, `/events`, `/ws`
- **models/types.go** — All domain types: `ClusterOverview`, `ResourceUsage`, `Insight`, `TopologyNode/Edge`, `ClusterInfoResponse`

### Frontend (`apps/web`)

React 18 + TypeScript + Vite + Tailwind CSS

Key libraries: TanStack Query (server state), TanStack Table, ReactFlow (cluster topology map), Lucide React (icons), React Router

23 views: Overview, Pods, Nodes, Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, Services, Ingresses, Gateways, HTTPRoutes, Endpoints, PVCs, PVs, StorageClasses, ConfigMaps, Secrets, HPAs, Namespaces, Events, RBAC, Settings + Cluster Map

Component organization: `src/components/{dashboard,map,resources,layout,shared,insights}/`
API client: `src/services/api.ts`
Type definitions: `src/types/kubernetes.ts`

### Data Flow

1. Cluster Manager reads kubeconfig contexts, connects to selected cluster
2. Shared informers watch K8s resources → in-memory lister caches
3. Dynamic client discovers Gateway API resources (with 5s timeout)
4. Metrics Collector polls Metrics Server → in-memory metrics cache
5. Insights Engine evaluates 12 rules against cluster state → recommendations
6. REST API serves enriched resource lists (with CPU/MEM metrics injected)
7. WebSocket hub broadcasts resource changes (debounced topology rebuilds)
8. Frontend uses TanStack Query with 30s refetch intervals

### Cluster Map

Two layout modes:
- **Grid**: compact grid of resources within namespace regions
- **Flow**: horizontal dependency chain (Ingress/Gateway → HTTPRoute → Service → Deployment → ReplicaSet → Pod)

Namespace regions are ReactFlow group nodes with child resource nodes. Supports filtering by resource type and namespace.

## CI

GitHub Actions (`.github/workflows/ci.yml`) on push/PR to `main`:
- Backend: `go build ./...` (Go 1.22, ubuntu-latest)
- Frontend: `npm ci && npm run build` (Node 20, ubuntu-latest)

## Key Reference

`docs/SPEC.md` contains the detailed technical specification including API endpoints, insights rules, data models, and Phase 2 roadmap. Consult it for feature work.
