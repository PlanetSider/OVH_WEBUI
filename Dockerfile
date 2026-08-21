# syntax=docker/dockerfile:1
# OVH_WEBUI 前后端一体镜像：Vite 静态资源嵌入 Go 二进制。

ARG GO_VERSION=1.25

FROM node:20-alpine AS frontend
WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts

COPY index.html vite.config.ts tsconfig*.json tailwind.config.ts postcss.config.js components.json ./
COPY public ./public
COPY src ./src

ENV NODE_ENV=production
RUN npm run build

FROM golang:${GO_VERSION}-bookworm AS backend
WORKDIR /src

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
COPY --from=frontend /app/dist ./web

RUN CGO_ENABLED=0 go build -tags ui -trimpath -ldflags="-s -w" -o /out/ovh-webui .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S ovh \
    && adduser -S -G ovh -h /app ovh

WORKDIR /app
COPY --from=backend /out/ovh-webui /app/ovh-webui
COPY backend/docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/ovh-webui /app/docker-entrypoint.sh \
    && mkdir -p /data/cache /data/logs \
    && chown -R ovh:ovh /app /data

ENV PORT=19998 \
    DATA_DIR=/data \
    GIN_MODE=release \
    ENABLE_API_KEY_AUTH=true \
    TZ=Asia/Shanghai

VOLUME ["/data"]
EXPOSE 19998
USER ovh

HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=5 \
  CMD wget -qO- "http://127.0.0.1:${PORT}/health" >/dev/null || exit 1

ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/ovh-webui"]
