#!/usr/bin/env bash
set -euo pipefail

HOST="${1:?usage: deploy.sh <ssh-host> <path-to-linux-binary> [key-for-models-check]}"
BINARY="${2:?usage: deploy.sh <ssh-host> <path-to-linux-binary> [key-for-models-check]}"
CLIENT_KEY="${3:-}"
VERSION="$(basename "$(dirname "$BINARY")")"
REMOTE_ROOT='cliproxyapi'

refuse() {
  echo "deploy refused: $*" >&2
  exit 1
}

command -v go >/dev/null 2>&1 || refuse "go is required to read the new binary's build info"
BUILD_INFO="$(go version -m "$BINARY")" || refuse "cannot read build info from $BINARY"
if printf '%s\n' "$BUILD_INFO" | grep -Eq '^[[:space:]]*build[[:space:]]+vcs\.modified=true$'; then
  refuse "the new binary was built from a modified checkout (vcs.modified=true)"
fi
NEW_COMMIT="$(printf '%s\n' "$BUILD_INFO" | sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.revision=\([0-9a-f]\{40\}\)$/\1/p' | sort -u)"
[[ "$NEW_COMMIT" =~ ^[0-9a-f]{40}$ ]] || refuse "the new binary must contain exactly one vcs.revision commit"

if ! RUNNING_COMMIT="$(ssh "$HOST" bash -s <<'REMOTE'
set -euo pipefail
active="$(readlink -f "$HOME/cliproxyapi/cli-proxy-api")"
commit_file="$(dirname "$active")/COMMIT"
file_commit=""
if [ -f "$commit_file" ]; then
  candidate="$(cat "$commit_file")"
  if [[ "$candidate" =~ ^[0-9a-f]{40}$ ]]; then
    file_commit="$candidate"
  fi
fi
embedded_commit=""
if revisions="$(LC_ALL=C grep -a -o 'vcs\.revision=[0-9a-f]\{40\}' "$active" | sort -u)"; then
  candidate="${revisions#vcs.revision=}"
  if [[ ! "$candidate" =~ ^[0-9a-f]{40}$ ]]; then
    echo "the active binary embeds conflicting vcs.revision values" >&2
    exit 1
  fi
  embedded_commit="$candidate"
fi
if [ -n "$file_commit" ] && [ -n "$embedded_commit" ] && [ "$file_commit" != "$embedded_commit" ]; then
  echo "COMMIT file says $file_commit but the active binary embeds $embedded_commit" >&2
  exit 1
fi
if [ -n "$embedded_commit" ]; then
  printf '%s\n' "$embedded_commit"
elif [ -n "$file_commit" ]; then
  printf '%s\n' "$file_commit"
else
  printf 'unidentified\n'
fi
REMOTE
)"; then
  refuse "cannot read the running commit on $HOST"
fi
[[ "$RUNNING_COMMIT" == unidentified || "$RUNNING_COMMIT" =~ ^[0-9a-f]{40}$ ]] || refuse "the running commit must be exactly one 40-hex commit"

command -v git >/dev/null 2>&1 || refuse "git is required for the ancestry check"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if ! REPO="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null)"; then
  [ -n "${CLIPROXY_REPO:-}" ] || refuse "the script is outside a git repository; set CLIPROXY_REPO to a local fork clone"
  REPO="$(git -C "$CLIPROXY_REPO" rev-parse --show-toplevel 2>/dev/null)" || refuse "CLIPROXY_REPO is not a git repository"
fi
git -C "$REPO" fetch --quiet origin || refuse "git fetch origin failed in $REPO"
git -C "$REPO" cat-file -e "$NEW_COMMIT^{commit}" 2>/dev/null || refuse "new commit $NEW_COMMIT is unknown in $REPO"
if [ "$RUNNING_COMMIT" = unidentified ]; then
  RUNNING_IS_ANCESTOR=false
  echo "the running commit cannot be identified from COMMIT or the active binary" >&2
elif ! git -C "$REPO" cat-file -e "$RUNNING_COMMIT^{commit}" 2>/dev/null; then
  RUNNING_IS_ANCESTOR=false
  echo "running commit $RUNNING_COMMIT is unknown in $REPO" >&2
elif git -C "$REPO" merge-base --is-ancestor "$RUNNING_COMMIT" "$NEW_COMMIT"; then
  RUNNING_IS_ANCESTOR=true
else
  ancestry_status=$?
  [ "$ancestry_status" -eq 1 ] || refuse "cannot compare running commit $RUNNING_COMMIT with new commit $NEW_COMMIT"
  RUNNING_IS_ANCESTOR=false
  echo "running commit $RUNNING_COMMIT is not an ancestor of new commit $NEW_COMMIT" >&2
  echo "commits that would be dropped (up to 20):" >&2
  git -C "$REPO" log --oneline -20 "$NEW_COMMIT..$RUNNING_COMMIT" >&2
fi
if [ "${DEPLOY_ALLOW_DROPPING_RUNNING+x}" = x ] && [ "$DEPLOY_ALLOW_DROPPING_RUNNING" != "$RUNNING_COMMIT" ]; then
  refuse "DEPLOY_ALLOW_DROPPING_RUNNING must equal running commit $RUNNING_COMMIT (new commit $NEW_COMMIT)"
fi
if [ "$RUNNING_IS_ANCESTOR" = false ]; then
  if [ "${DEPLOY_ALLOW_DROPPING_RUNNING:-}" != "$RUNNING_COMMIT" ]; then
    refuse "set DEPLOY_ALLOW_DROPPING_RUNNING=$RUNNING_COMMIT only to deploy over a running commit that is not verified as contained"
  fi
  echo "WARNING: DEPLOY_ALLOW_DROPPING_RUNNING=$RUNNING_COMMIT; deploying $NEW_COMMIT without proof that the running commit is contained" >&2
fi

ssh "$HOST" mkdir -p "$REMOTE_ROOT/$VERSION"
scp "$BINARY" "$HOST:$REMOTE_ROOT/$VERSION/cli-proxy-api.new"
ssh "$HOST" bash -s "$VERSION" "$NEW_COMMIT" <<'REMOTE'
set -euo pipefail
version="$1"
commit="$2"
root="$HOME/cliproxyapi"
mv "$root/$version/cli-proxy-api.new" "$root/$version/cli-proxy-api"
chmod +x "$root/$version/cli-proxy-api"
printf '%s\n' "$commit" > "$root/$version/COMMIT"
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
