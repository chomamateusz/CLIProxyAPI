#!/usr/bin/env bash
set -euo pipefail

HOST="${1:?usage: deploy.sh <ssh-host> <path-to-linux-binary> [key-for-models-check]}"
BINARY="${2:?usage: deploy.sh <ssh-host> <path-to-linux-binary> [key-for-models-check]}"
CLIENT_KEY="${3:-}"
VERSION="$(basename "$(dirname "$BINARY")")"
REMOTE_ROOT='cliproxyapi'

ssh "$HOST" "mkdir -p $REMOTE_ROOT/$VERSION"
scp "$BINARY" "$HOST:$REMOTE_ROOT/$VERSION/cli-proxy-api.new"
ssh "$HOST" bash -s "$VERSION" <<'REMOTE'
set -euo pipefail
version="$1"
root="$HOME/cliproxyapi"
mv "$root/$version/cli-proxy-api.new" "$root/$version/cli-proxy-api"
chmod +x "$root/$version/cli-proxy-api"
previous="$(readlink "$root/cli-proxy-api" || true)"
printf '%s\n' "$previous" > "$root/.previous-binary"
ln -sfn "$root/$version/cli-proxy-api" "$root/cli-proxy-api"
systemctl --user restart cliproxyapi
sleep 2
systemctl --user is-active cliproxyapi
REMOTE

echo "== /v1/models without key (expect 401)"
curl -s -o /dev/null -w '%{http_code}\n' "http://$HOST:8317/v1/models"
if [ -n "$CLIENT_KEY" ]; then
  echo "== /v1/models with key (expect 200 and a model list)"
  curl -s -w '\nHTTP %{http_code}\n' -H "Authorization: Bearer $CLIENT_KEY" "http://$HOST:8317/v1/models" | head -c 1500
  echo
fi
echo "deployed $VERSION; previous binary recorded in ~/cliproxyapi/.previous-binary"
