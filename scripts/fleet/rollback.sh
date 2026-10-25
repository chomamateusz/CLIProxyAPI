#!/usr/bin/env bash
set -euo pipefail

HOST="${1:?usage: rollback.sh <ssh-host> [version, default 8.0.39]}"
VERSION="${2:-8.0.39}"

ssh "$HOST" bash -s "$VERSION" <<'REMOTE'
set -euo pipefail
version="$1"
root="$HOME/cliproxyapi"
target="$root/$version/cli-proxy-api"
if [ ! -x "$target" ]; then
  echo "no binary at $target" >&2
  exit 1
fi
ln -sfn "$target" "$root/cli-proxy-api"
systemctl --user restart cliproxyapi
sleep 2
systemctl --user is-active cliproxyapi
echo "rolled back to $version"
REMOTE

echo "== /v1/models without key (expect 401)"
curl -s -o /dev/null -w '%{http_code}\n' "http://$HOST:8317/v1/models"
