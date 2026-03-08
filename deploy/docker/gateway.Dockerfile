FROM node:22-alpine AS builder

WORKDIR /build
RUN corepack enable pnpm

# Copy workspace root files and all workspace package.json files
COPY package.json pnpm-workspace.yaml ./
COPY api/gateway/package.json api/gateway/
COPY api/webhook/package.json api/webhook/
COPY dashboard/package.json dashboard/

RUN pnpm install --filter @settla/gateway...

# Copy only gateway source (not the whole dir to avoid node_modules collisions)
COPY api/gateway/tsconfig.json api/gateway/
COPY api/gateway/src/ api/gateway/src/

RUN pnpm --filter @settla/gateway build

# ── Production dependencies only ─────────────────────────────────
RUN pnpm deploy --filter @settla/gateway --prod --legacy /prod/gateway

# ── Runtime ──────────────────────────────────────────────────────
FROM node:22-slim

WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends wget && rm -rf /var/lib/apt/lists/*

COPY --from=builder /prod/gateway/node_modules ./node_modules
COPY --from=builder /build/api/gateway/dist ./dist
COPY --from=builder /prod/gateway/package.json ./

# Proto files needed at runtime for gRPC client (loaded via @grpc/proto-loader)
COPY proto/ /proto/

EXPOSE 3000
CMD ["node", "dist/index.js"]
