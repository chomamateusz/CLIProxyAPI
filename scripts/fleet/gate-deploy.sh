#!/usr/bin/env bash
set -euo pipefail

USAGE='usage: gate-deploy.sh <path>/fleet-<8hex>/cli-proxy-api | --status | --rollback'
GATE_KEY="${GATE_KEY:-$HOME/.ssh/proxy-deploy_ed25519}"
GATE_HOST="${GATE_HOST:-chomamateusz-agent--proxy@127.0.0.1}"

refuse() {
  echo "gate deploy refused: $*" >&2
  exit 1
}

gate() {
  ssh -i "$GATE_KEY" -o IdentitiesOnly=yes -o BatchMode=yes "$GATE_HOST" "$@"
}

sha256_of() {
  local line
  if command -v sha256sum >/dev/null 2>&1; then
    line="$(sha256sum -- "$1")"
  else
    line="$(shasum -a 256 -- "$1")"
  fi
  printf '%s' "${line%% *}"
}

[ "$#" -eq 1 ] || refuse "$USAGE"
case "$1" in
  --status)
    gate status
    exit
    ;;
  --rollback)
    gate rollback
    exit
    ;;
  -*) refuse "$USAGE" ;;
esac

BINARY="$1"
VERSION="$(basename "$(dirname "$BINARY")")"
[ "$(basename "$BINARY")" = cli-proxy-api ] || refuse "$USAGE"
[[ "$VERSION" =~ ^fleet-[0-9a-f]{8}$ ]] || refuse "the binary's directory must be named fleet-<8 hex>, got $VERSION"
[ -f "$BINARY" ] || refuse "no binary at $BINARY"

command -v go >/dev/null 2>&1 || refuse "go is required to read the new binary's build info"
BUILD_INFO="$(go version -m "$BINARY")" || refuse "cannot read build info from $BINARY"
if printf '%s\n' "$BUILD_INFO" | grep -Eq '^[[:space:]]*build[[:space:]]+vcs\.modified=true$'; then
  refuse "the new binary was built from a modified checkout (vcs.modified=true)"
fi
NEW_COMMIT="$(printf '%s\n' "$BUILD_INFO" | sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.revision=\([0-9a-f]\{40\}\)$/\1/p' | sort -u)"
[[ "$NEW_COMMIT" =~ ^[0-9a-f]{40}$ ]] || refuse "the new binary must contain exactly one vcs.revision commit"
[ "$VERSION" = "fleet-${NEW_COMMIT:0:8}" ] || refuse "directory $VERSION does not match revision $NEW_COMMIT (expected fleet-${NEW_COMMIT:0:8})"

GATE_STATUS="$(gate status)" || refuse "cannot read the gate status from $GATE_HOST"
RUNNING_COMMIT="$(printf '%s\n' "$GATE_STATUS" | sed -n 's/^commit=//p')"
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
  echo "the running commit cannot be identified from the gate status" >&2
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

SHA256="$(sha256_of "$BINARY")"
echo "== deploying $VERSION (commit $NEW_COMMIT, sha256 $SHA256) through $GATE_HOST"
if gate deploy "$VERSION" "$SHA256" "$NEW_COMMIT" < "$BINARY"; then
  DEPLOY_STATUS=0
else
  DEPLOY_STATUS=$?
fi
echo "== gate status"
gate status || true
exit "$DEPLOY_STATUS"
