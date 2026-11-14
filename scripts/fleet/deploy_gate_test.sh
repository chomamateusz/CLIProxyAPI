#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
GATE="$SCRIPT_DIR/deploy-gate.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-${HOME}/repositories/claude-tmp}/cliproxy-deploy-gate-test.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_CEILING_DIRECTORIES="$TEST_ROOT" GIT_ALLOW_PROTOCOL=file
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GATE_KEY GATE_HOST CLIPROXY_REPO DEPLOY_ALLOW_DROPPING_RUNNING
mkdir -p "$TEST_ROOT/shims" "$TEST_ROOT/client-shims" "$TEST_ROOT/no-curl"

cat > "$TEST_ROOT/shims/systemctl" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
case_dir="$HOME/.."
printf 'systemctl %s\n' "$*" >> "$case_dir/events"
printf '%s\n' "${XDG_RUNTIME_DIR-}" > "$case_dir/xdg"
[ -z "${GATE_LEAK_MARKER+x}${http_proxy+x}" ] || echo env-leak >> "$case_dir/events"
[ "$1" = --user ] || exit 94
if [ "$2" = is-active ]; then
  active="$(readlink -f "$HOME/cliproxyapi/cli-proxy-api" || true)"
  if [ -z "$active" ] || grep -q unhealthy "$active"; then
    echo failed
    exit 3
  fi
  echo active
fi
SHIM
cat > "$TEST_ROOT/shims/curl" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
case_dir="$HOME/.."
printf 'curl %s\n' "$*" >> "$case_dir/events"
[ -z "${GATE_LEAK_MARKER+x}${http_proxy+x}" ] || echo env-leak >> "$case_dir/events"
case "$*" in *management*) exit 95 ;; esac
[ "${*: -1}" = http://127.0.0.1:8317/v1/models ] || exit 96
active="$(readlink -f "$HOME/cliproxyapi/cli-proxy-api")"
if grep -Eq 'unhealthy|silent' "$active"; then
  printf 000
  exit 7
fi
printf 401
SHIM
cat > "$TEST_ROOT/shims/sleep" <<'SHIM'
#!/usr/bin/env bash
SHIM
cat > "$TEST_ROOT/client-shims/ssh" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -lt 8 ] || [ "$1" != -i ] || [ "$2" != "$EXPECT_GATE_KEY" ] || [ "$3" != -o ] || [ "$4" != IdentitiesOnly=yes ] \
  || [ "$5" != -o ] || [ "$6" != BatchMode=yes ] || [ "$7" != "$EXPECT_GATE_HOST" ]; then
  echo "unexpected ssh invocation: $*" >&2
  exit 90
fi
shift 7
printf 'ssh %s\n' "$*" >> "$CLIENT_EVENTS"
exec env -i HOME="$CASE_HOME" PATH="$GATE_PATH" SSH_ORIGINAL_COMMAND="$*" /bin/bash "$GATE"
SHIM
cat > "$TEST_ROOT/client-shims/go" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
[ "$#" -eq 3 ] && [ "$1" = version ] && [ "$2" = -m ] && [ -f "$3" ] || exit 93
cat "$TEST_BUILD_INFO"
SHIM
chmod +x "$TEST_ROOT/shims/"* "$TEST_ROOT/client-shims/"*
for tool in bash env date mkdir head od tr sha256sum mv chmod ln readlink rm rmdir wc flock cat grep; do
  ln -s "$(command -v "$tool")" "$TEST_ROOT/no-curl/$tool"
done
ln -s "$TEST_ROOT/shims/systemctl" "$TEST_ROOT/no-curl/systemctl"
ln -s "$TEST_ROOT/shims/sleep" "$TEST_ROOT/no-curl/sleep"
export GATE="$GATE"
export GATE_PATH="$TEST_ROOT/shims:/usr/bin:/bin"

OLD_COMMIT=1111111100000000000000000000000000000000
NEW_VERSION=fleet-22222222
NEW_COMMIT=22222222aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
SECRET=SECRET-TOKEN-VALUE-must-never-leak
CASES=0

fail() {
  echo "FAIL: $CASE_NAME: $*" >&2
  if [ -f "$CASE_ROOT/output" ]; then
    cat "$CASE_ROOT/output" >&2
  fi
  exit 1
}

pass() {
  echo "PASS: $CASE_NAME"
}

write_elf() {
  mkdir -p "$(dirname "$1")"
  printf '\177ELF fake binary: %s\n' "$2" > "$1"
}

sha_of() {
  local line
  line="$(sha256sum "$1")"
  printf '%s' "${line%% *}"
}

new_case() {
  CASE_NAME="$1"
  CASES=$((CASES + 1))
  CASE_ROOT="$TEST_ROOT/case-$CASES"
  export CASE_HOME="$CASE_ROOT/home"
  export CLIENT_EVENTS="$CASE_ROOT/client-events"
  R="$CASE_HOME/cliproxyapi"
  OLD_TARGET="$R/fleet-11111111/cli-proxy-api"
  LEGACY_TARGET="$R/8.0.39/cli-proxy-api"
  NEW_TARGET="$R/$NEW_VERSION/cli-proxy-api"
  write_elf "$OLD_TARGET" 'old fleet build'
  chmod 0755 "$OLD_TARGET"
  printf '%s\n' "$OLD_COMMIT" > "$R/fleet-11111111/COMMIT"
  write_elf "$LEGACY_TARGET" 'legacy build'
  chmod 0755 "$LEGACY_TARGET"
  ln -s "$OLD_TARGET" "$R/cli-proxy-api"
  printf '%s\n' "$LEGACY_TARGET" > "$R/.previous-binary"
  mkdir -p "$CASE_HOME/.cli-proxy-api"
  printf '%s\n' "$SECRET" > "$CASE_HOME/.cli-proxy-api/claude-token.json"
  printf 'api-keys: [%s]\n' "$SECRET" > "$CASE_HOME/config.yaml"
  : > "$CASE_ROOT/events"
  : > "$CLIENT_EVENTS"
  GATE_STDIN=/dev/null
  GATE_RUN_PATH="$GATE_PATH"
  EXTRA_ENV=()
  NEW_BINARY="$CASE_ROOT/upload/$NEW_VERSION/cli-proxy-api"
  write_elf "$NEW_BINARY" 'new fleet build'
  NEW_SHA="$(sha_of "$NEW_BINARY")"
}

snapshot() {
  (
    cd "$CASE_HOME"
    find . \( -path ./cliproxyapi/deploy-gate.log -o -path ./cliproxyapi/.deploy-gate.lock \) -prune -o -printf '%p %y %m %s %l\n' | sort
    find . \( -path ./cliproxyapi/deploy-gate.log -o -path ./cliproxyapi/.deploy-gate.lock \) -prune -o -type f -exec sha256sum {} + | sort
  )
}

run_gate_env() {
  snapshot > "$CASE_ROOT/before"
  if env -i HOME="$CASE_HOME" PATH="$GATE_RUN_PATH" "${EXTRA_ENV[@]}" "$@" /bin/bash "$GATE" < "$GATE_STDIN" > "$CASE_ROOT/output" 2>&1; then
    STATUS=0
  else
    STATUS=$?
  fi
  if grep -Fq "$SECRET" "$CASE_ROOT/output"; then
    fail "secret leaked into the output"
  fi
}

run_gate() {
  run_gate_env SSH_ORIGINAL_COMMAND="$1"
}

expect_output() {
  grep -Fq -- "$1" "$CASE_ROOT/output" || fail "missing output: $1"
}

expect_status() {
  [ "$STATUS" -eq "$1" ] || fail "exited $STATUS, expected $1"
}

expect_active() {
  [ "$(readlink "$R/cli-proxy-api")" = "$1" ] || fail "active symlink is $(readlink "$R/cli-proxy-api"), expected $1"
}

expect_previous() {
  [ "$(cat "$R/.previous-binary")" = "$1" ] || fail "previous binary is $(cat "$R/.previous-binary"), expected $1"
}

expect_restarts() {
  local count
  count="$(grep -c '^systemctl --user restart cliproxyapi$' "$CASE_ROOT/events" || true)"
  [ "$count" -eq "$1" ] || fail "expected $1 restarts, saw $count"
}

last_log_line() {
  tail -n 1 "$R/deploy-gate.log"
}

expect_log() {
  last_log_line | grep -Eq -- "$1" || fail "log line does not match $1: $(last_log_line)"
}

expect_untouched() {
  snapshot > "$CASE_ROOT/after"
  diff "$CASE_ROOT/before" "$CASE_ROOT/after" >&2 || fail "the gate home changed"
  expect_restarts 0
}

expect_refused() {
  expect_status 1
  expect_output "refused: $1"
  expect_untouched
  expect_log 'result=refused: '
  [ "$(wc -l < "$R/deploy-gate.log")" -eq 1 ] || fail "expected exactly one log line"
  pass
}

refusal_case() {
  new_case "refuses: $1"
  run_gate "$2"
  expect_refused "$3"
}

LONG_COMMAND="status $(printf 'a%.0s' $(seq 1 300))"
BAD_SHA_UPPER="$(printf 'A%.0s' $(seq 1 64))"
SOME_SHA="$(printf 'b%.0s' $(seq 1 64))"

new_case 'status prints the active binary, its commit and the previous binary'
run_gate status
expect_status 0
printf 'active=active\nbinary=%s\nversion=fleet-11111111\ncommit=%s\nprevious=%s\n' "$OLD_TARGET" "$OLD_COMMIT" "$LEGACY_TARGET" > "$CASE_ROOT/expected"
diff "$CASE_ROOT/expected" "$CASE_ROOT/output" >&2 || fail 'unexpected status output'
expect_untouched
expect_log '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z verb=status result=ok$'
pass

new_case 'status of a legacy binary without COMMIT reports unidentified'
ln -sfn "$LEGACY_TARGET" "$R/cli-proxy-api"
run_gate status
expect_status 0
expect_output 'version=8.0.39'
expect_output 'commit=unidentified'
pass

new_case 'deploy installs the binary, records COMMIT and the previous binary, switches and restarts'
GATE_STDIN="$NEW_BINARY"
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_status 0
cmp "$NEW_BINARY" "$NEW_TARGET" || fail 'binary not installed'
[ "$(stat -c %a "$NEW_TARGET")" = 755 ] || fail 'binary is not 0755'
[ "$(cat "$R/$NEW_VERSION/COMMIT")" = "$NEW_COMMIT" ] || fail 'COMMIT not written'
[ ! -e "$R/$NEW_VERSION/cli-proxy-api.new" ] || fail 'partial upload left behind'
expect_active "$NEW_TARGET"
expect_previous "$OLD_TARGET"
expect_restarts 1
grep -qx 'systemctl --user is-active cliproxyapi' "$CASE_ROOT/events" || fail 'is-active not checked'
grep -q '^curl .*http://127.0.0.1:8317/v1/models$' "$CASE_ROOT/events" || fail 'models check missing'
[ "$(cat "$CASE_ROOT/xdg")" = "/run/user/$(id -u)" ] || fail 'XDG_RUNTIME_DIR not derived from the uid'
expect_output "deployed $NEW_VERSION (commit $NEW_COMMIT); previous binary: $OLD_TARGET"
expect_log "verb=deploy version=$NEW_VERSION sha256=$NEW_SHA commit=$NEW_COMMIT result=deployed$"
pass

new_case 'redeploying the active version reuses the binary and keeps the previous binary'
GATE_STDIN="$NEW_BINARY"
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_status 0
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_status 0
expect_output 'reusing the identical existing binary'
expect_active "$NEW_TARGET"
expect_previous "$OLD_TARGET"
expect_restarts 2
pass

new_case 'an existing inactive version with the same sha256 is reused'
mkdir -p "$R/$NEW_VERSION"
cp "$NEW_BINARY" "$NEW_TARGET"
GATE_STDIN="$NEW_BINARY"
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_status 0
expect_output 'reusing the identical existing binary'
expect_active "$NEW_TARGET"
expect_previous "$OLD_TARGET"
pass

new_case 'hostile environment variables do not change the gate'
GATE_STDIN="$NEW_BINARY"
EXTRA_ENV=('BASH_FUNC_systemctl%%=() { echo hijacked; }' GATE_LEAK_MARKER=1 http_proxy=http://127.0.0.1:1 LC_ALL=tr_TR.UTF-8 XDG_RUNTIME_DIR=/tmp/elsewhere)
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_status 0
expect_active "$NEW_TARGET"
expect_restarts 1
if grep -q hijacked "$CASE_ROOT/output"; then fail 'an inherited function replaced systemctl'; fi
if grep -q env-leak "$CASE_ROOT/events"; then fail 'client environment reached a child process'; fi
[ "$(cat "$CASE_ROOT/xdg")" = "/run/user/$(id -u)" ] || fail 'XDG_RUNTIME_DIR came from the environment'
pass

new_case 'missing curl falls back to is-active and says so'
GATE_STDIN="$NEW_BINARY"
GATE_RUN_PATH="$TEST_ROOT/no-curl"
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_status 0
expect_output 'curl not found; the health check relies on systemctl is-active only'
expect_active "$NEW_TARGET"
if grep -q '^curl' "$CASE_ROOT/events"; then fail 'curl was called'; fi
pass

refusal_case 'unknown verb' hello 'unknown verb'
refusal_case 'empty command' '' 'empty command'
refusal_case 'whitespace-only command' '   ' 'empty command'
refusal_case 'extra field on status' 'status now' 'status takes no arguments'
refusal_case 'extra field on rollback' 'rollback now' 'rollback takes no arguments'
refusal_case 'missing deploy field' "deploy $NEW_VERSION $SOME_SHA" 'deploy takes exactly'
refusal_case 'extra deploy field' "deploy $NEW_VERSION $SOME_SHA $NEW_COMMIT extra" 'deploy takes exactly'
refusal_case 'shell metacharacters' 'status; rm -rf ~' 'command contains characters outside'
refusal_case 'chained deploy' "deploy $NEW_VERSION $SOME_SHA $NEW_COMMIT; rm -rf ~" 'command contains characters outside'
refusal_case 'command substitution' 'status $(id)' 'command contains characters outside'
refusal_case 'backticks' 'status `id`' 'command contains characters outside'
refusal_case 'pipe' 'status | cat' 'command contains characters outside'
refusal_case 'embedded newline' $'status\nrollback' 'command contains characters outside'
refusal_case 'tab separator' $'status\trollback' 'command contains characters outside'
refusal_case 'path traversal in version' "deploy ../.cli-proxy-api $SOME_SHA $NEW_COMMIT" 'command contains characters outside'
refusal_case 'slash in version' "deploy fleet-22222222/../x $SOME_SHA $NEW_COMMIT" 'command contains characters outside'
refusal_case 'non-hex version' "deploy fleet-2222222g $SOME_SHA $NEW_COMMIT" 'version must match'
refusal_case 'short version' "deploy fleet-2222222 $SOME_SHA $NEW_COMMIT" 'version must match'
refusal_case 'uppercase sha' "deploy $NEW_VERSION $BAD_SHA_UPPER $NEW_COMMIT" 'command contains characters outside'
refusal_case 'short sha' "deploy $NEW_VERSION abcdef $NEW_COMMIT" 'sha256 must be 64'
refusal_case 'short commit' "deploy $NEW_VERSION $SOME_SHA 22222222" 'commit must be'
refusal_case 'commit not matching the version' "deploy $NEW_VERSION $SOME_SHA 33333333aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" 'commit 33333333aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa does not match version'
refusal_case 'scp upload' 'scp -t cliproxyapi/fleet-22222222/cli-proxy-api' 'command contains characters outside'
refusal_case 'sftp server' '/usr/lib/openssh/sftp-server' 'command contains characters outside'
refusal_case 'internal sftp' 'internal-sftp' 'unknown verb'
refusal_case 'overlong command' "$LONG_COMMAND" 'command is longer than'

new_case 'refuses: interactive session without a command'
run_gate_env
expect_refused 'no command given'

new_case 'refuses: sha256 mismatch'
GATE_STDIN="$NEW_BINARY"
run_gate "deploy $NEW_VERSION $SOME_SHA $NEW_COMMIT"
expect_refused 'sha256 of the received binary does not match'

new_case 'refuses: non-ELF binary'
printf 'not an executable\n' > "$NEW_BINARY"
GATE_STDIN="$NEW_BINARY"
run_gate "deploy $NEW_VERSION $(sha_of "$NEW_BINARY") $NEW_COMMIT"
expect_refused 'the received binary is not an ELF file'

new_case 'refuses: empty stdin'
run_gate "deploy $NEW_VERSION $SOME_SHA $NEW_COMMIT"
expect_refused 'no binary on stdin'

new_case 'refuses: stdin larger than 256 MiB'
head -c $((256 * 1024 * 1024 + 1)) /dev/zero > "$CASE_ROOT/oversize"
GATE_STDIN="$CASE_ROOT/oversize"
run_gate "deploy $NEW_VERSION $SOME_SHA $NEW_COMMIT"
rm -f "$CASE_ROOT/oversize"
expect_refused 'binary is larger than'

new_case 'refuses: existing version with a different sha256'
write_elf "$NEW_TARGET" 'a different build of the same version'
GATE_STDIN="$NEW_BINARY"
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_refused "$NEW_VERSION already exists with a different sha256"

new_case 'refuses: existing version recording a different commit'
mkdir -p "$R/$NEW_VERSION"
printf '22222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n' > "$R/$NEW_VERSION/COMMIT"
GATE_STDIN="$NEW_BINARY"
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_refused "$NEW_VERSION already records commit"

new_case 'refusal log line strips control characters and caps the command'
run_gate $'status\n\033[31mrm -rf ~'"$LONG_COMMAND"
expect_status 1
[ "$(wc -l < "$R/deploy-gate.log")" -eq 1 ] || fail 'log injection produced extra lines'
if LC_ALL=C grep -q $'\033' "$R/deploy-gate.log"; then fail 'control character logged'; fi
logged_command="$(sed -n 's/.* command="\([^"]*\)" result=.*/\1/p' "$R/deploy-gate.log")"
[ -n "$logged_command" ] || fail 'command not logged'
[ "${#logged_command}" -le 160 ] || fail "logged command is ${#logged_command} characters"
pass

new_case 'refuses: missing ~/cliproxyapi'
rm -rf "$R"
run_gate status
expect_status 1
expect_output 'refused: ~/cliproxyapi is missing'
[ ! -e "$R" ] || fail 'cliproxyapi was created'
pass

new_case 'a failed unit health check rolls back automatically and exits 3'
write_elf "$NEW_BINARY" 'unhealthy build'
NEW_SHA="$(sha_of "$NEW_BINARY")"
GATE_STDIN="$NEW_BINARY"
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_status 3
expect_output "deploy failed, rolled back to $OLD_TARGET"
expect_active "$OLD_TARGET"
expect_previous "$LEGACY_TARGET"
expect_restarts 2
expect_log "verb=deploy version=$NEW_VERSION .* result=deploy failed, rolled back to $OLD_TARGET$"
pass

new_case 'a unit that is active but does not answer 401 also rolls back'
write_elf "$NEW_BINARY" 'silent build'
NEW_SHA="$(sha_of "$NEW_BINARY")"
GATE_STDIN="$NEW_BINARY"
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_status 3
expect_output "deploy failed, rolled back to $OLD_TARGET"
expect_active "$OLD_TARGET"
pass

new_case 'rollback toggles between the active and the legacy previous binary'
run_gate rollback
expect_status 0
expect_active "$LEGACY_TARGET"
expect_previous "$OLD_TARGET"
expect_output "rolled back to $LEGACY_TARGET; previous binary: $OLD_TARGET"
expect_log "verb=rollback target=$LEGACY_TARGET result=rolled back$"
run_gate rollback
expect_status 0
expect_active "$OLD_TARGET"
expect_previous "$LEGACY_TARGET"
expect_restarts 2
pass

new_case 'rollback after a deploy returns to the replaced binary'
GATE_STDIN="$NEW_BINARY"
run_gate "deploy $NEW_VERSION $NEW_SHA $NEW_COMMIT"
expect_status 0
GATE_STDIN=/dev/null
run_gate rollback
expect_status 0
expect_active "$OLD_TARGET"
expect_previous "$NEW_TARGET"
pass

new_case 'rollback accepts a relative legacy previous binary'
printf '8.0.39/cli-proxy-api\n' > "$R/.previous-binary"
run_gate rollback
expect_status 0
expect_active "$LEGACY_TARGET"
expect_previous "$OLD_TARGET"
pass

new_case 'rollback to an unhealthy previous binary restores the active one and exits 3'
write_elf "$LEGACY_TARGET" 'unhealthy legacy build'
run_gate rollback
expect_status 3
expect_output "rollback failed, restored $OLD_TARGET"
expect_active "$OLD_TARGET"
expect_previous "$LEGACY_TARGET"
pass

rollback_refusal_case() {
  new_case "refuses rollback: $1"
  write_elf "$CASE_HOME/cli-proxy-api" 'binary outside cliproxyapi'
  write_elf "$CASE_HOME/.cli-proxy-api/cli-proxy-api" 'binary inside the secrets directory'
  chmod 0755 "$CASE_HOME/cli-proxy-api" "$CASE_HOME/.cli-proxy-api/cli-proxy-api"
  local previous="${2//@R@/$R}"
  printf '%s\n' "${previous//@HOME@/$CASE_HOME}" > "$R/.previous-binary"
  run_gate rollback
  expect_refused "$3"
}

rollback_refusal_case 'nothing recorded' '' 'no previous binary recorded'
rollback_refusal_case 'path outside cliproxyapi' @HOME@/cli-proxy-api 'the previous binary is not an executable'
rollback_refusal_case 'system file' /bin/sh 'the previous binary is not an executable'
rollback_refusal_case 'secrets directory' @HOME@/.cli-proxy-api/cli-proxy-api 'the previous binary is not an executable'
rollback_refusal_case 'parent directory name' @R@/../cli-proxy-api 'the previous binary is not an executable'
rollback_refusal_case 'relative parent directory name' ../cli-proxy-api 'the previous binary is not an executable'
rollback_refusal_case 'dot directory name' @R@/./cli-proxy-api 'the previous binary is not an executable'
rollback_refusal_case 'nested traversal' @R@/fleet-11111111/../../.cli-proxy-api/cli-proxy-api 'the previous binary is not an executable'
rollback_refusal_case 'missing binary' @R@/fleet-99999999/cli-proxy-api 'the previous binary is not an executable'
rollback_refusal_case 'already active' @R@/fleet-11111111/cli-proxy-api 'the previous binary is already active'

new_case 'refuses rollback: previous binary not executable'
chmod 0644 "$LEGACY_TARGET"
run_gate rollback
expect_refused 'the previous binary is not an executable'

new_case 'calls are serialised on the lock file'
flock "$R/.deploy-gate.lock" -c "touch '$CASE_ROOT/held'; /usr/bin/sleep 2" &
holder=$!
until [ -e "$CASE_ROOT/held" ]; do /usr/bin/sleep 0.05; done
started=$SECONDS
run_gate status
wait "$holder"
expect_status 0
[ $((SECONDS - started)) -ge 1 ] || fail 'status did not wait for the lock'
pass

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
cp "$SCRIPT_DIR/gate-deploy.sh" "$REPO/scripts/fleet/gate-deploy.sh"
CLIENT="$REPO/scripts/fleet/gate-deploy.sh"

new_client_case() {
  new_case "client: $1"
  export TEST_BUILD_INFO="$CASE_ROOT/build-info"
  export EXPECT_GATE_KEY="$CASE_ROOT/client-home/.ssh/proxy-deploy_ed25519"
  export EXPECT_GATE_HOST=chomamateusz-agent--proxy@127.0.0.1
  unset GATE_KEY GATE_HOST DEPLOY_ALLOW_DROPPING_RUNNING
  mkdir -p "$CASE_ROOT/client-home"
  set_running "$BASE"
  CLIENT_BINARY="$CASE_ROOT/build/fleet-${NEW:0:8}/cli-proxy-api"
  write_elf "$CLIENT_BINARY" 'client build'
  printf '\tbuild\tvcs.revision=%s\n\tbuild\tvcs.modified=false\n' "$NEW" > "$TEST_BUILD_INFO"
  CLIENT_TARGET="$R/fleet-${NEW:0:8}/cli-proxy-api"
}

set_running() {
  local dir="$R/fleet-${1:0:8}"
  write_elf "$dir/cli-proxy-api" "running $1"
  chmod 0755 "$dir/cli-proxy-api"
  printf '%s\n' "$1" > "$dir/COMMIT"
  ln -sfn "$dir/cli-proxy-api" "$R/cli-proxy-api"
  RUNNING_TARGET="$dir/cli-proxy-api"
}

run_client() {
  snapshot > "$CASE_ROOT/before"
  if HOME="$CASE_ROOT/client-home" PATH="$TEST_ROOT/client-shims:$PATH" bash "$CLIENT" "$@" > "$CASE_ROOT/output" 2>&1; then
    STATUS=0
  else
    STATUS=$?
  fi
}

expect_client_deployed() {
  expect_status 0
  cmp "$CLIENT_BINARY" "$CLIENT_TARGET" || fail 'binary not installed by the gate'
  [ "$(cat "$R/fleet-${NEW:0:8}/COMMIT")" = "$NEW" ] || fail 'COMMIT not written'
  expect_active "$CLIENT_TARGET"
  expect_previous "$RUNNING_TARGET"
  printf 'ssh status\nssh deploy fleet-%s %s %s\nssh status\n' "${NEW:0:8}" "$(sha_of "$CLIENT_BINARY")" "$NEW" > "$CASE_ROOT/expected-ssh"
  diff "$CASE_ROOT/expected-ssh" "$CLIENT_EVENTS" >&2 || fail 'unexpected ssh calls'
  expect_output "deployed fleet-${NEW:0:8} (commit $NEW)"
  expect_output '== gate status'
  expect_output "commit=$NEW"
}

expect_client_refused() {
  expect_status 1
  expect_output "gate deploy refused: $1"
  expect_untouched
  if grep -q '^ssh deploy' "$CLIENT_EVENTS"; then fail 'deploy reached the gate'; fi
}

expect_no_ssh() {
  [ ! -s "$CLIENT_EVENTS" ] || fail 'ssh was called'
}

new_client_case 'an ancestor running commit deploys through the gate'
run_client "$CLIENT_BINARY"
expect_client_deployed
pass

new_client_case 'GATE_KEY and GATE_HOST select the key and host'
export GATE_KEY="$CASE_ROOT/other-key" GATE_HOST=someone@127.0.0.1
export EXPECT_GATE_KEY="$GATE_KEY" EXPECT_GATE_HOST="$GATE_HOST"
run_client "$CLIENT_BINARY"
expect_client_deployed
pass

new_client_case 'a modified build is refused before ssh'
printf '\tbuild\tvcs.revision=%s\n\tbuild\tvcs.modified=true\n' "$NEW" > "$TEST_BUILD_INFO"
run_client "$CLIENT_BINARY"
expect_client_refused 'the new binary was built from a modified checkout'
expect_no_ssh
pass

new_client_case 'a directory not matching the revision is refused before ssh'
CLIENT_BINARY="$CASE_ROOT/build/fleet-${BASE:0:8}/cli-proxy-api"
write_elf "$CLIENT_BINARY" 'misplaced build'
run_client "$CLIENT_BINARY"
expect_client_refused "directory fleet-${BASE:0:8} does not match revision $NEW"
expect_no_ssh
pass

new_client_case 'a binary outside a fleet-<8hex> directory is refused'
mkdir -p "$CASE_ROOT/build/8.0.39"
cp "$CLIENT_BINARY" "$CASE_ROOT/build/8.0.39/cli-proxy-api"
run_client "$CASE_ROOT/build/8.0.39/cli-proxy-api"
expect_client_refused "the binary's directory must be named fleet-<8 hex>"
expect_no_ssh
pass

new_client_case 'a divergent running commit is refused without an override'
set_running "$DIVERGED"
run_client "$CLIENT_BINARY"
expect_client_refused "set DEPLOY_ALLOW_DROPPING_RUNNING=$DIVERGED"
expect_output 'running-only feature must not disappear'
[ "$(cat "$CLIENT_EVENTS")" = 'ssh status' ] || fail 'only the read-only status call may reach the gate'
pass

new_client_case 'a wrong override is refused'
set_running "$DIVERGED"
export DEPLOY_ALLOW_DROPPING_RUNNING="$BASE"
run_client "$CLIENT_BINARY"
expect_client_refused 'DEPLOY_ALLOW_DROPPING_RUNNING must equal running commit'
pass

new_client_case 'the exact override deploys over a divergent running commit'
set_running "$DIVERGED"
export DEPLOY_ALLOW_DROPPING_RUNNING="$DIVERGED"
run_client "$CLIENT_BINARY"
expect_client_deployed
expect_output "WARNING: DEPLOY_ALLOW_DROPPING_RUNNING=$DIVERGED"
pass

new_client_case 'an unidentified running binary is refused without an override'
ln -sfn "$LEGACY_TARGET" "$R/cli-proxy-api"
run_client "$CLIENT_BINARY"
expect_client_refused 'set DEPLOY_ALLOW_DROPPING_RUNNING=unidentified'
pass

new_client_case 'override unidentified deploys over a legacy binary'
ln -sfn "$LEGACY_TARGET" "$R/cli-proxy-api"
RUNNING_TARGET="$LEGACY_TARGET"
export DEPLOY_ALLOW_DROPPING_RUNNING=unidentified
run_client "$CLIENT_BINARY"
expect_client_deployed
pass

new_client_case 'a new commit unknown to the repository is refused'
CLIENT_BINARY="$CASE_ROOT/build/fleet-aaaaaaaa/cli-proxy-api"
write_elf "$CLIENT_BINARY" 'unknown build'
printf '\tbuild\tvcs.revision=%s\n\tbuild\tvcs.modified=false\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa > "$TEST_BUILD_INFO"
run_client "$CLIENT_BINARY"
expect_client_refused 'new commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa is unknown'
pass

new_client_case 'a failed gate health check propagates exit 3 and still prints the status'
write_elf "$CLIENT_BINARY" 'unhealthy client build'
run_client "$CLIENT_BINARY"
expect_status 3
expect_output 'deploy failed, rolled back to'
expect_output '== gate status'
expect_active "$RUNNING_TARGET"
pass

new_client_case '--status passes through to the gate'
run_client --status
expect_status 0
expect_output "commit=$BASE"
[ "$(cat "$CLIENT_EVENTS")" = 'ssh status' ] || fail 'unexpected ssh calls'
pass

new_client_case '--rollback passes through to the gate'
run_client --rollback
expect_status 0
expect_active "$LEGACY_TARGET"
expect_previous "$RUNNING_TARGET"
[ "$(cat "$CLIENT_EVENTS")" = 'ssh rollback' ] || fail 'unexpected ssh calls'
pass

for usage_args in '' '--bogus' "two args"; do
  new_client_case "usage error: '${usage_args}'"
  read -r -a args <<< "$usage_args"
  run_client "${args[@]}"
  expect_client_refused 'usage: gate-deploy.sh'
  expect_no_ssh
  pass
done

echo "All $CASES deploy gate tests passed."
