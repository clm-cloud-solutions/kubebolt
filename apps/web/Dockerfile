# Build stage: always runs on native amd64 (no QEMU)
# Node.js + npm ci + Vite build is extremely slow under QEMU emulation
FROM --platform=$BUILDPLATFORM node:20-alpine AS builder

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

# Runtime stage: multi-arch (nginx with static files is tiny)
FROM nginx:alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 3000
