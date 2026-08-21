#!/usr/bin/env bash
# Linux 服务器一键 Docker 部署
# 用法：
#   chmod +x scripts/docker-deploy.sh
#   ./scripts/docker-deploy.sh              # 拉取镜像并后台启动
#   ./scripts/docker-deploy.sh --down
#   ./scripts/docker-deploy.sh --logs
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

ACTION=up

for a in "$@"; do
  case "$a" in
    --down) ACTION=down ;;
    --logs) ACTION=logs ;;
    -h|--help)
      echo "Usage: $0 [--down|--logs]"
      exit 0
      ;;
  esac
done

if [[ ! -f .env ]]; then
  echo "==> creating .env from .env.example"
  cp .env.example .env
  if command -v openssl >/dev/null 2>&1; then
    KEY="$(openssl rand -hex 32)"
    # portable sed
    if sed --version >/dev/null 2>&1; then
      sed -i "s/API_SECRET_KEY=.*/API_SECRET_KEY=$KEY/" .env
    else
      sed -i '' "s/API_SECRET_KEY=.*/API_SECRET_KEY=$KEY/" .env
    fi
    echo "    generated API_SECRET_KEY (see .env)"
  else
    echo "    WARN: set API_SECRET_KEY in .env manually"
  fi
fi

COMPOSE=(docker compose)

case "$ACTION" in
  down)
    "${COMPOSE[@]}" down
    ;;
  logs)
    "${COMPOSE[@]}" logs -f --tail=200
    ;;
  up)
    "${COMPOSE[@]}" pull
    "${COMPOSE[@]}" up -d
    echo ""
    echo "----------------------------------------"
    echo " Deployed"
    echo "----------------------------------------"
    echo " UI:  http://$(hostname -I 2>/dev/null | awk '{print $1}'):19998"
    echo " Health: curl -s http://127.0.0.1:19998/health"
    echo " Login key:  grep API_SECRET_KEY .env"
    echo " Logs:       $0 --logs"
    echo ""
    "${COMPOSE[@]}" ps
    ;;
esac
