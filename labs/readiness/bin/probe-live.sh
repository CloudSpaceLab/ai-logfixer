#!/usr/bin/env bash
set -euo pipefail

services=(
  "php-laravel http://127.0.0.1:18081/orders/readiness"
  "python-fastapi http://127.0.0.1:18082/orders/readiness"
  "python-django http://127.0.0.1:18083/orders/readiness"
  "node-express http://127.0.0.1:18084/orders/readiness"
  "ruby-rails http://127.0.0.1:18085/orders/readiness"
)

for service in "${services[@]}"; do
  name="${service%% *}"
  url="${service#* }"
  status="$(curl -sS -o /tmp/ai-logfixer-readiness-body -w "%{http_code}" "$url" || true)"
  body="$(cat /tmp/ai-logfixer-readiness-body 2>/dev/null || true)"
  printf "%-16s status=%s body=%s\n" "$name" "$status" "$body"
done
