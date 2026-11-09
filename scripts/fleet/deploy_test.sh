#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TEST_ROOT="$(mktemp -d "${TMPDIR:-${HOME}/repositories/claude-tmp}/cliproxy-deploy-test.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_CEILING_DIRECTORIES="$TEST_ROOT" GIT_ALLOW_PROTOCOL=file
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
mkdir -p "$TEST_ROOT/shims" "$TEST_ROOT/outside"

cat > "$TEST_ROOT/shims/ssh" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
[ "$1" = 127.0.0.1 ] || exit 90
shift
if [ "$1" = bash ] && [ "$2" = -s ]; then
  if [ "$#" -eq 2 ]; then
    echo ssh-read >> "$TEST_EVENTS"
  else
    echo ssh-mutate >> "$TEST_EVENTS"
  fi
  cd "$TEST_REMOTE_HOME"
  HOME="$TEST_REMOTE_HOME" bash "${@:2}"
else
  echo ssh-mutate >> "$TEST_EVENTS"
  cd "$TEST_REMOTE_HOME"
  HOME="$TEST_REMOTE_HOME" bash -c "$*"
fi
SHIM
cat > "$TEST_ROOT/shims/scp" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
echo scp >> "$TEST_EVENTS"
[ "${2%%:*}" = 127.0.0.1 ] || exit 91
cp "$1" "$TEST_REMOTE_HOME/${2#*:}"
SHIM
cat > "$TEST_ROOT/shims/curl" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
printf 'curl %s\n' "$*" >> "$TEST_EVENTS"
grep -qx 'systemctl --user is-active cliproxyapi' "$TEST_EVENTS" || exit 92
printf '401\n'
SHIM
cat > "$TEST_ROOT/shims/go" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
[ "$#" -eq 3 ] && [ "$1" = version ] && [ "$2" = -m ] && [ -f "$3" ] || exit 93
[ "${TEST_GO_FAIL:-0}" = 0 ] || exit 1
cat "$TEST_BUILD_INFO"
SHIM
cat > "$TEST_ROOT/shims/systemctl" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
printf 'systemctl %s\n' "$*" >> "$TEST_EVENTS"
SHIM
cat > "$TEST_ROOT/shims/sleep" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
[ "$1" = 2 ]
SHIM
chmod +x "$TEST_ROOT/shims/"*
export PATH="$TEST_ROOT/shims:$PATH"

git init --quiet --bare "$TEST_ROOT/origin.git"
git init --quiet --initial-branch=main "$TEST_ROOT/repo"
REPO="$TEST_ROOT/repo"
git -C "$REPO" config user.name Deploy-test
git -C "$REPO" config user.email deploy-test@example.invalid
printf 'base\n' > "$REPO/feature"
git -C "$REPO" add feature
git -C "$REPO" commit --quiet -m 'base feature'
BASE="$(git -C "$REPO" rev-parse HEAD)"
printf 'new\n' > "$REPO/feature"
git -C "$REPO" commit --quiet -am 'new main feature'
NEW="$(git -C "$REPO" rev-parse HEAD)"
git -C "$REPO" checkout --quiet -b running-only "$BASE"
printf 'keep this feature\n' > "$REPO/running-feature"
git -C "$REPO" add running-feature
git -C "$REPO" commit --quiet -m 'running-only feature must not disappear'
DIVERGED="$(git -C "$REPO" rev-parse HEAD)"
git -C "$REPO" checkout --quiet main
git -C "$REPO" remote add origin "$TEST_ROOT/origin.git"
git -C "$REPO" push --quiet --all origin
mkdir -p "$REPO/scripts/fleet"
cp "$SCRIPT_DIR/deploy.sh" "$REPO/scripts/fleet/deploy.sh"
cp "$SCRIPT_DIR/deploy.sh" "$TEST_ROOT/outside/deploy.sh"
UNKNOWN=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
CASES=0

fail() {
  echo "FAIL: $CASE_NAME: $*" >&2
  if [ -f "$CASE_ROOT/output" ]; then
    cat "$CASE_ROOT/output" >&2
  fi
  exit 1
}

new_case() {
  CASE_NAME="$1"
  CASES=$((CASES + 1))
  CASE_ROOT="$TEST_ROOT/case-$CASES"
  export TEST_REMOTE_HOME="$CASE_ROOT/home"
  export TEST_EVENTS="$CASE_ROOT/events"
  export TEST_BUILD_INFO="$CASE_ROOT/build-info"
  export TEST_GO_FAIL=0
  unset CLIPROXY_REPO DEPLOY_ALLOW_DROPPING_RUNNING
  DEPLOY_SCRIPT="$REPO/scripts/fleet/deploy.sh"
  BINARY="$CASE_ROOT/new-version/cli-proxy-api"
  mkdir -p "$TEST_REMOTE_HOME/cliproxyapi/old-version" "$(dirname "$BINARY")"
  : > "$TEST_EVENTS"
  printf 'fake new binary\n' > "$BINARY"
  printf '\tbuild\tvcs.revision=%s\n\tbuild\tvcs.modified=false\n' "$NEW" > "$TEST_BUILD_INFO"
  printf 'fake active binary\000vcs.revision=%s\n' "$BASE" > "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api"
  printf '%s\n' "$BASE" > "$TEST_REMOTE_HOME/cliproxyapi/old-version/COMMIT"
  ln -s "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api" "$TEST_REMOTE_HOME/cliproxyapi/cli-proxy-api"
}

set_running() {
  printf '%s\n' "$1" > "$TEST_REMOTE_HOME/cliproxyapi/old-version/COMMIT"
  printf 'fake active binary\000vcs.revision=%s\n' "$1" > "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api"
}

run_deploy() {
  if bash "$DEPLOY_SCRIPT" 127.0.0.1 "$BINARY" > "$CASE_ROOT/output" 2>&1; then
    STATUS=0
  else
    STATUS=$?
  fi
}

expect_output() {
  grep -Fq -- "$1" "$CASE_ROOT/output" || fail "missing output: $1"
}

expect_deployed() {
  [ "$STATUS" -eq 0 ] || fail "deploy exited $STATUS"
  [ "$(cat "$TEST_REMOTE_HOME/cliproxyapi/new-version/COMMIT")" = "$NEW" ] || fail "COMMIT not written"
  cmp "$BINARY" "$TEST_REMOTE_HOME/cliproxyapi/new-version/cli-proxy-api" || fail "binary not copied"
  [ "$(readlink "$TEST_REMOTE_HOME/cliproxyapi/cli-proxy-api")" = "$TEST_REMOTE_HOME/cliproxyapi/new-version/cli-proxy-api" ] || fail "active symlink not switched"
  [ "$(cat "$TEST_REMOTE_HOME/cliproxyapi/.previous-binary")" = "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api" ] || fail "previous binary not recorded"
  [ "$(grep -c '^ssh-read$' "$TEST_EVENTS")" -eq 1 ] || fail "expected one read-only ssh call"
  grep -q '^scp$' "$TEST_EVENTS" || fail "scp was not called"
  grep -q '^curl .*http://127.0.0.1:8317/v1/models' "$TEST_EVENTS" || fail "post-deploy model check not preserved"
  expect_output 'deployed new-version; previous binary recorded in ~/cliproxyapi/.previous-binary'
  echo "PASS: $CASE_NAME"
}

expect_refused() {
  [ "$STATUS" -ne 0 ] || fail "deploy unexpectedly succeeded"
  if grep -Eq '^(scp|ssh-mutate|systemctl|curl)( |$)' "$TEST_EVENTS"; then
    fail "refused deploy touched the host or sent an HTTP request"
  fi
  [ ! -e "$TEST_REMOTE_HOME/cliproxyapi/new-version" ] || fail "remote directory created"
  [ "$(readlink "$TEST_REMOTE_HOME/cliproxyapi/cli-proxy-api")" = "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api" ] || fail "active symlink changed"
  expect_output 'deploy refused:'
  echo "PASS: $CASE_NAME"
}

new_case 'running ancestor deploys and writes COMMIT without a pre-deploy HTTP request'
run_deploy
expect_deployed

new_case 'running equals new deploys'
set_running "$NEW"
run_deploy
expect_deployed

new_case 'divergent running commit refuses and lists the dropped feature'
set_running "$DIVERGED"
run_deploy
expect_refused
expect_output "$NEW"
expect_output "$DIVERGED"
expect_output 'running-only feature must not disappear'

new_case 'unknown running commit refuses without an override'
set_running "$UNKNOWN"
run_deploy
expect_refused
expect_output "running commit $UNKNOWN is unknown"
expect_output "set DEPLOY_ALLOW_DROPPING_RUNNING=$UNKNOWN"

new_case 'exact override permits a deploy over an unknown running commit'
set_running "$UNKNOWN"
export DEPLOY_ALLOW_DROPPING_RUNNING="$UNKNOWN"
run_deploy
expect_deployed
expect_output "WARNING: DEPLOY_ALLOW_DROPPING_RUNNING=$UNKNOWN"

new_case 'COMMIT disagreeing with the embedded revision refuses even with an override'
printf 'vcs.revision=%s\n' "$DIVERGED" > "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api"
export DEPLOY_ALLOW_DROPPING_RUNNING="$BASE"
run_deploy
expect_refused
expect_output "COMMIT file says $BASE but the active binary embeds $DIVERGED"

new_case 'COMMIT alone identifies a binary without an embedded revision'
printf 'no build metadata\n' > "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api"
run_deploy
expect_deployed

new_case 'embedded revision fallback without COMMIT accepts duplicate agreeing matches'
mv "$TEST_REMOTE_HOME/cliproxyapi/old-version/COMMIT" "$CASE_ROOT/saved-COMMIT"
printf 'binary\000vcs.revision=%s\nother\000vcs.revision=%s\n' "$BASE" "$BASE" > "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api"
run_deploy
expect_deployed

new_case 'an unusable COMMIT falls back to the embedded revision'
printf 'not a commit\n' > "$TEST_REMOTE_HOME/cliproxyapi/old-version/COMMIT"
run_deploy
expect_deployed

new_case 'conflicting embedded revisions refuse'
mv "$TEST_REMOTE_HOME/cliproxyapi/old-version/COMMIT" "$CASE_ROOT/saved-COMMIT"
printf 'vcs.revision=%s\nvcs.revision=%s\n' "$BASE" "$DIVERGED" > "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api"
export DEPLOY_ALLOW_DROPPING_RUNNING="$BASE"
run_deploy
expect_refused
expect_output 'conflicting vcs.revision values'

new_case 'neither source refuses without an override'
mv "$TEST_REMOTE_HOME/cliproxyapi/old-version/COMMIT" "$CASE_ROOT/saved-COMMIT"
printf 'no build metadata\n' > "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api"
run_deploy
expect_refused
expect_output 'cannot be identified'
expect_output 'set DEPLOY_ALLOW_DROPPING_RUNNING=unidentified'

new_case 'override unidentified permits a deploy when neither source exists'
mv "$TEST_REMOTE_HOME/cliproxyapi/old-version/COMMIT" "$CASE_ROOT/saved-COMMIT"
printf 'no build metadata\n' > "$TEST_REMOTE_HOME/cliproxyapi/old-version/cli-proxy-api"
export DEPLOY_ALLOW_DROPPING_RUNNING=unidentified
run_deploy
expect_deployed
expect_output 'WARNING: DEPLOY_ALLOW_DROPPING_RUNNING=unidentified'

new_case 'modified new binary refuses before SSH'
printf '\tbuild\tvcs.revision=%s\n\tbuild\tvcs.modified=true\n' "$NEW" > "$TEST_BUILD_INFO"
run_deploy
expect_refused
expect_output 'vcs.modified=true'
[ ! -s "$TEST_EVENTS" ] || fail 'dirty binary reached SSH'

new_case 'new binary without a revision refuses'
printf '\tbuild\tvcs.modified=false\n' > "$TEST_BUILD_INFO"
run_deploy
expect_refused
expect_output 'exactly one vcs.revision'

new_case 'unreadable build info refuses'
export TEST_GO_FAIL=1
run_deploy
expect_refused
expect_output 'cannot read build info'

new_case 'missing go refuses before SSH'
mkdir -p "$TEST_ROOT/no-go"
for executable in bash basename dirname; do
  ln -s "$(command -v "$executable")" "$TEST_ROOT/no-go/$executable"
done
PATH="$TEST_ROOT/no-go" run_deploy
expect_refused
expect_output 'go is required'
[ ! -s "$TEST_EVENTS" ] || fail 'missing go reached SSH'

new_case 'missing git refuses without mutating the host'
mkdir -p "$TEST_ROOT/no-git"
for executable in bash basename dirname go ssh cat readlink grep sort sed; do
  ln -s "$(command -v "$executable")" "$TEST_ROOT/no-git/$executable"
done
PATH="$TEST_ROOT/no-git" run_deploy
expect_refused
expect_output 'git is required'

new_case 'exact override permits a divergent deploy with a warning'
set_running "$DIVERGED"
export DEPLOY_ALLOW_DROPPING_RUNNING="$DIVERGED"
run_deploy
expect_deployed
expect_output "WARNING: DEPLOY_ALLOW_DROPPING_RUNNING=$DIVERGED"
expect_output 'running-only feature must not disappear'

new_case 'wrong override refuses a divergent deploy'
set_running "$DIVERGED"
export DEPLOY_ALLOW_DROPPING_RUNNING="$BASE"
run_deploy
expect_refused
expect_output 'DEPLOY_ALLOW_DROPPING_RUNNING must equal running commit'
expect_output 'running-only feature must not disappear'

new_case 'wrong override refuses even an ancestor deploy'
export DEPLOY_ALLOW_DROPPING_RUNNING="$NEW"
run_deploy
expect_refused

new_case 'an explicitly empty override refuses'
export DEPLOY_ALLOW_DROPPING_RUNNING=''
run_deploy
expect_refused

new_case 'copied script uses CLIPROXY_REPO'
DEPLOY_SCRIPT="$TEST_ROOT/outside/deploy.sh"
export CLIPROXY_REPO="$REPO"
run_deploy
expect_deployed

new_case 'copied script without CLIPROXY_REPO refuses'
DEPLOY_SCRIPT="$TEST_ROOT/outside/deploy.sh"
run_deploy
expect_refused
expect_output 'set CLIPROXY_REPO'

new_case 'copied script with invalid CLIPROXY_REPO refuses'
DEPLOY_SCRIPT="$TEST_ROOT/outside/deploy.sh"
export CLIPROXY_REPO="$TEST_ROOT/outside"
run_deploy
expect_refused
expect_output 'CLIPROXY_REPO is not a git repository'

new_case 'repository containing the script takes precedence over CLIPROXY_REPO'
export CLIPROXY_REPO="$TEST_ROOT/outside"
run_deploy
expect_deployed

new_case 'failed fetch refuses'
git -C "$REPO" remote set-url origin "$TEST_ROOT/missing-origin.git"
run_deploy
expect_refused
expect_output 'git fetch origin failed'
git -C "$REPO" remote set-url origin "$TEST_ROOT/origin.git"

new_case 'fetch makes a running commit from origin available before checking ancestry'
git clone --quiet --branch main "$TEST_ROOT/origin.git" "$TEST_ROOT/other-clone"
git -C "$TEST_ROOT/other-clone" config user.name Deploy-test
git -C "$TEST_ROOT/other-clone" config user.email deploy-test@example.invalid
printf 'remote only\n' > "$TEST_ROOT/other-clone/remote-feature"
git -C "$TEST_ROOT/other-clone" add remote-feature
git -C "$TEST_ROOT/other-clone" commit --quiet -m 'remote-only feature must not disappear'
REMOTE_ONLY="$(git -C "$TEST_ROOT/other-clone" rev-parse HEAD)"
git -C "$TEST_ROOT/other-clone" push --quiet origin main
set_running "$REMOTE_ONLY"
if git -C "$REPO" cat-file -e "$REMOTE_ONLY^{commit}" 2>/dev/null; then
  fail 'remote-only commit already exists locally'
fi
run_deploy
expect_refused
expect_output 'remote-only feature must not disappear'
git -C "$REPO" cat-file -e "$REMOTE_ONLY^{commit}" || fail 'fetch did not retrieve the running commit'

echo "All $CASES deploy tests passed."
