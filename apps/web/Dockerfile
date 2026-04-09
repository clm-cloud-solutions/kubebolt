# Build stage: always runs on native amd64 (no QEMU)
# Node.js + npm ci + Vite build is extremely slow under QEMU emulation
FROM --platform=$BUILDPLATFORM node:20-alpine AS builder

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

# Runtime stage: multi-arch (nginx with static files is tiny)
FROM nginx:1.27-alpine3.21
RUN apk upgrade --no-cache
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY docker-entrypoint.sh /docker-entrypoint.d/40-api-backend.sh
RUN chmod +x /docker-entrypoint.d/40-api-backend.sh
ENV API_BACKEND=api:8080
EXPOSE 3000
