#!/bin/sh
set -eu

rm -f /tmp/serving-stale-config
if [ -f /app/runtime/restart-required ]; then
  echo "starting with stale runtime state; a service restart should clear this"
  touch /tmp/serving-stale-config
  rm -f /app/runtime/restart-required
fi

exec /server
