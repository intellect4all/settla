FROM node:22-alpine AS builder

WORKDIR /app
RUN corepack enable pnpm
COPY pnpm-workspace.yaml package.json pnpm-lock.yaml* ./
COPY api/webhook/package.json api/webhook/
RUN pnpm install --frozen-lockfile --filter @settla/webhook
COPY api/webhook/ api/webhook/
RUN pnpm --filter @settla/webhook build

FROM node:22-alpine
WORKDIR /app
RUN corepack enable pnpm
COPY --from=builder /app/api/webhook/dist ./dist
COPY --from=builder /app/api/webhook/package.json ./
COPY --from=builder /app/node_modules ./node_modules
RUN addgroup -g 1000 settla && adduser -u 1000 -G settla -D settla
USER settla

EXPOSE 3001
CMD ["node", "dist/index.js"]
