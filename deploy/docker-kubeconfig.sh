#!/bin/sh
#
# Generates a kubeconfig compatible with Docker containers.
#
# Problem: Docker Desktop's built-in Kubernetes uses 127.0.0.1:6443 as the
# API server address. Inside a container, 127.0.0.1 refers to the container
# itself, not the host. This script rewrites the address to
# kubernetes.docker.internal, which is a SAN in Docker Desktop's K8s
# TLS certificate and resolves to the host from within containers.
#
# Usage:
#   ./deploy/docker-kubeconfig.sh
#
# The generated file is written to /tmp/docker-kubeconfig and is mounted
# by docker-compose.yml automatically.
#
# Note: This is only needed for Docker Desktop's built-in Kubernetes.
# Remote clusters (EKS, GKE, AKS, etc.) use routable endpoints and
# do not need this rewrite.

set -e

KUBECONFIG_SRC="${KUBECONFIG:-$HOME/.kube/config}"
KUBECONFIG_DST="/tmp/docker-kubeconfig"

if [ ! -f "$KUBECONFIG_SRC" ]; then
  echo "Error: kubeconfig not found at $KUBECONFIG_SRC" >&2
  exit 1
fi

sed \
  's|https://127.0.0.1|https://kubernetes.docker.internal|g
   s|https://localhost|https://kubernetes.docker.internal|g' \
  "$KUBECONFIG_SRC" > "$KUBECONFIG_DST"

echo "Generated $KUBECONFIG_DST (rewrote localhost → kubernetes.docker.internal)"
