#!/bin/sh
# Container entrypoint: ensure DATA_DIR exists, then exec main as PID 1 (SIGTERM).
set -eu

DATA_DIR="${DATA_DIR:-/data}"
mkdir -p "${DATA_DIR}/cache" "${DATA_DIR}/logs" 2>/dev/null || true

key="${API_SECRET_KEY:-}"
if [ "${#key}" -lt 8 ] || [ "${#key}" -gt 256 ]; then
  echo "ERROR: API_SECRET_KEY must be 8-256 characters." >&2
  exit 1
fi
case "$key" in *[A-Z]*) ;; *) echo "ERROR: API_SECRET_KEY must contain an English uppercase letter." >&2; exit 1 ;; esac
case "$key" in *[a-z]*) ;; *) echo "ERROR: API_SECRET_KEY must contain an English lowercase letter." >&2; exit 1 ;; esac
case "$key" in *[0-9]*) ;; *) echo "ERROR: API_SECRET_KEY must contain a digit." >&2; exit 1 ;; esac

if [ "${ALLOW_INSECURE_DEFAULT_KEY:-}" = "true" ]; then
  echo "ERROR: ALLOW_INSECURE_DEFAULT_KEY is not supported." >&2
  exit 1
fi
if [ "${ENABLE_API_KEY_AUTH:-}" = "false" ]; then
  echo "ERROR: ENABLE_API_KEY_AUTH=false is not supported; API authentication is mandatory." >&2
  exit 1
fi
if [ "${TG_WEBHOOK_SECRET_OPTIONAL:-}" = "true" ]; then
  echo "ERROR: TG_WEBHOOK_SECRET_OPTIONAL=true is not supported; Telegram webhook authentication is mandatory." >&2
  exit 1
fi

echo "ovh-webui starting PORT=${PORT:-19998} DATA_DIR=${DATA_DIR}"
exec "$@"
