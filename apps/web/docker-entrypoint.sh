#!/bin/sh
# Replace api:8080 with the API_BACKEND env var in the nginx config.
# Default is "api:8080" (Docker Compose). Helm sets it to the K8s service name.
sed -i "s|api:8080|${API_BACKEND}|g" /etc/nginx/conf.d/default.conf
