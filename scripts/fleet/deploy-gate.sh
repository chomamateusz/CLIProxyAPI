#!/bin/bash
set -euo pipefail

for inherited_function in $(compgen -A function); do
  unset -f "$inherited_function"
done
trap '' PIPE HUP

RAW_COMMAND="${SSH_ORIGINAL_COMMAND-}"
COMMAND_GIVEN="${SSH_ORIGINAL_COMMAND+x}"
for inherited_variable in $(compgen -e); do
  case "$inherited_variable" in
    HOME | PATH) ;;
    *) unset "$inherited_variable" 2>/dev/null || true ;;
  esac
done
export LC_ALL=C
IFS=$' \t\n'
umask 022
export XDG_RUNTIME_DIR="/run/user/$UID"

ROOT="$HOME/cliproxyapi"
ACTIVE_LINK="$ROOT/cli-proxy-api"
PREVIOUS_FILE="$ROOT/.previous-binary"
LOCK_FILE="$ROOT/.deploy-gate.lock"
LOG_FILE="$ROOT/deploy-gate.log"
UNIT=cliproxyapi
HEALTH_URL=http://127.0.0.1:8317/v1/models
HEALTH_SECONDS=30
LOCK_WAIT_SECONDS=600
MAX_COMMAND_LENGTH=256
MAX_LOGGED_COMMAND_LENGTH=160
MAX_BINARY_BYTES=$((256 * 1024 * 1024))
UPLOAD_SECONDS=300

LOG_READY=""
LOG_FIELDS=""
LOG_RESULT=""
PARTIAL_FILES=()
CREATED_DIR=""

sanitized_command() {
  local accepted_only="${RAW_COMMAND//[!a-z0-9 -]/?}"
  printf '%s' "${accepted_only:0:$MAX_LOGGED_COMMAND_LENGTH}"
}

say() {
  printf '%s\n' "$*" 2>/dev/null || true
}

say_err() {
  printf '%s\n' "$*" >&2 2>/dev/null || true
}

log_call() {
  printf '%s %s result=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$LOG_FIELDS" "$1" >> "$LOG_FILE"
}

finish() {
  local status=$?
  local file
  for file in "${PARTIAL_FILES[@]}"; do
    rm -f -- "$file"
  done
  if [ -n "$CREATED_DIR" ]; then
    rmdir -- "$CREATED_DIR" 2>/dev/null || true
  fi
  if [ -n "$LOG_READY" ]; then
    log_call "${LOG_RESULT:-error exit $status}" || true
  fi
  exit "$status"
}

refuse() {
  LOG_RESULT="refused: $1"
  say_err "refused: $1"
  exit 1
}

fail_unhealthy() {
  LOG_RESULT="$1"
  say_err "$1"
  exit 3
}

sha256_of() {
  local line
  line="$(sha256sum -- "$1")"
  printf '%s' "${line%% *}"
}

starts_with_elf_magic() {
  [ "$(head -c 4 -- "$1" | od -An -tx1 | tr -d ' \n')" = 7f454c46 ]
}

first_line_of() {
  local line=""
  if [ -f "$1" ]; then
    IFS= read -r line < "$1" || true
  fi
  printf '%s' "$line"
}

absolute_target() {
  case "$1" in
    "") ;;
    /*) printf '%s' "$1" ;;
    *) printf '%s' "$ROOT/$1" ;;
  esac
}

current_target() {
  local target
  if target="$(readlink -- "$ACTIVE_LINK")"; then
    absolute_target "$target"
  fi
}

recorded_previous() {
  first_line_of "$PREVIOUS_FILE"
}

record_previous() {
  rm -f -- "$PREVIOUS_FILE.new"
  printf '%s\n' "$1" > "$PREVIOUS_FILE.new"
  mv -f -- "$PREVIOUS_FILE.new" "$PREVIOUS_FILE"
}

is_valid_rollback_target() {
  local target="$1"
  [[ "$target" == "$ROOT/"* ]] || return 1
  local relative="${target#"$ROOT/"}"
  [[ "$relative" =~ ^([A-Za-z0-9._-]+)/cli-proxy-api$ ]] || return 1
  local dir="${BASH_REMATCH[1]}"
  [ "$dir" != . ] && [ "$dir" != .. ] && [ -f "$target" ] && [ -x "$target" ]
}

commit_of_target() {
  local commit
  commit="$(first_line_of "${1%/*}/COMMIT")"
  if [[ "$commit" =~ ^[0-9a-f]{40}$ ]]; then
    printf '%s' "$commit"
  else
    printf 'unidentified'
  fi
}

unit_state() {
  local state
  state="$(systemctl --user is-active "$UNIT" 2>/dev/null || true)"
  printf '%s' "${state:-unknown}"
}

switch_to() {
  ln -sfn -- "$1" "$ACTIVE_LINK"
}

restart_unit() {
  systemctl --user restart "$UNIT" || say_err "systemctl restart $UNIT reported an error"
}

check_health() {
  local start=$SECONDS attempt=0 state="" code="" have_curl=yes
  if ! command -v curl >/dev/null 2>&1; then
    have_curl=""
    say "curl not found; the health check relies on systemctl is-active only"
  fi
  while [ "$attempt" -lt "$HEALTH_SECONDS" ] && [ $((SECONDS - start)) -lt "$HEALTH_SECONDS" ]; do
    attempt=$((attempt + 1))
    state="$(unit_state)"
    if [ "$state" = active ]; then
      if [ -z "$have_curl" ]; then
        say "health: $UNIT is active"
        return 0
      fi
      code="$(curl -q -s -o /dev/null -w '%{http_code}' --connect-timeout 1 --max-time 2 "$HEALTH_URL" || true)"
      if [ "$code" = 401 ]; then
        say "health: $UNIT is active and $HEALTH_URL answered 401"
        return 0
      fi
    fi
    sleep 1
  done
  say_err "health check failed: $UNIT is $state, $HEALTH_URL answered ${code:-nothing}"
  return 1
}

run_status() {
  LOG_FIELDS="verb=status"
  local binary target version="" commit=unidentified
  binary="$(readlink -- "$ACTIVE_LINK" || true)"
  if [ -n "$binary" ]; then
    target="$(absolute_target "$binary")"
    version="${target%/*}"
    version="${version##*/}"
    commit="$(commit_of_target "$target")"
  fi
  printf 'active=%s\nbinary=%s\nversion=%s\ncommit=%s\nprevious=%s\n' \
    "$(unit_state)" "$binary" "$version" "$commit" "$(recorded_previous)" || true
  LOG_RESULT=ok
}

receive_binary() {
  local incoming="$1" size received=0
  rm -f -- "$incoming"
  PARTIAL_FILES+=("$incoming")
  timeout "$UPLOAD_SECONDS" head -c "$((MAX_BINARY_BYTES + 1))" > "$incoming" || received=$?
  [ "$received" -ne 124 ] || refuse "the binary did not arrive within $UPLOAD_SECONDS s"
  [ "$received" -eq 0 ] || refuse "reading the binary from stdin failed"
  size=$(($(wc -c < "$incoming")))
  [ "$size" -le "$MAX_BINARY_BYTES" ] || refuse "binary is larger than $MAX_BINARY_BYTES bytes"
  [ "$size" -gt 0 ] || refuse "no binary on stdin"
}

prepare_version_dir() {
  local version="$1" sha="$2" commit="$3"
  local dir="$ROOT/$version"
  local final="$dir/cli-proxy-api"
  [ ! -L "$dir" ] || refuse "$version is a symlink"
  if [ -e "$dir" ] && [ ! -d "$dir" ]; then
    refuse "$version exists and is not a directory"
  fi
  [ ! -L "$final" ] || refuse "$version/cli-proxy-api is a symlink"
  if [ -e "$final" ] && [ "$(sha256_of "$final")" != "$sha" ]; then
    refuse "$version already exists with a different sha256; versions are immutable"
  fi
  local existing_commit
  existing_commit="$(first_line_of "$dir/COMMIT")"
  if [ -n "$existing_commit" ] && [ "$existing_commit" != "$commit" ]; then
    refuse "$version already records commit $existing_commit"
  fi
  if [ ! -d "$dir" ]; then
    mkdir -- "$dir"
    CREATED_DIR="$dir"
  fi
}

install_binary() {
  local dir="$1" commit="$2"
  local incoming="$dir/cli-proxy-api.new" final="$dir/cli-proxy-api"
  if [ -e "$final" ]; then
    rm -f -- "$incoming"
    say "reusing the identical existing binary $final"
  else
    mv -f -- "$incoming" "$final"
  fi
  chmod 0755 -- "$final"
  rm -f -- "$dir/COMMIT"
  printf '%s\n' "$commit" > "$dir/COMMIT"
  PARTIAL_FILES=()
  CREATED_DIR=""
}

run_deploy() {
  local version="$1" sha="$2" commit="$3"
  [[ "$version" =~ ^fleet-[0-9a-f]{8}$ ]] || refuse "version must match fleet-<8 lowercase hex>"
  [[ "$sha" =~ ^[0-9a-f]{64}$ ]] || refuse "sha256 must be 64 lowercase hex"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || refuse "commit must be 40 lowercase hex"
  [ "${commit:0:8}" = "${version#fleet-}" ] || refuse "commit $commit does not match version $version"
  LOG_FIELDS="verb=deploy version=$version sha256=$sha commit=$commit"

  local dir="$ROOT/$version"
  local incoming="$dir/cli-proxy-api.new" new_target="$dir/cli-proxy-api"
  prepare_version_dir "$version" "$sha" "$commit"
  receive_binary "$incoming"
  [ "$(sha256_of "$incoming")" = "$sha" ] || refuse "sha256 of the received binary does not match $sha"
  starts_with_elf_magic "$incoming" || refuse "the received binary is not an ELF file"
  install_binary "$dir" "$commit"

  local current previous_before fallback=""
  current="$(current_target)"
  previous_before="$(recorded_previous)"
  if [ -n "$current" ] && [ "$current" != "$new_target" ]; then
    record_previous "$current"
    fallback="$current"
  elif is_valid_rollback_target "$(absolute_target "$previous_before")"; then
    fallback="$(absolute_target "$previous_before")"
  fi
  switch_to "$new_target"
  restart_unit
  if check_health; then
    LOG_RESULT=deployed
    say "deployed $version (commit $commit); previous binary: $(recorded_previous)"
    return
  fi

  [ -n "$fallback" ] || fail_unhealthy "deploy failed, no previous binary to roll back to"
  switch_to "$fallback"
  record_previous "$previous_before"
  restart_unit
  if check_health; then
    fail_unhealthy "deploy failed, rolled back to $fallback"
  fi
  fail_unhealthy "deploy failed, rolled back to $fallback, which is not healthy either"
}

run_rollback() {
  LOG_FIELDS="verb=rollback"
  local target current
  target="$(absolute_target "$(recorded_previous)")"
  [ -n "$target" ] || refuse "no previous binary recorded"
  is_valid_rollback_target "$target" || refuse "the previous binary is not an executable ~/cliproxyapi/<dir>/cli-proxy-api"
  current="$(current_target)"
  [ "$target" != "$current" ] || refuse "the previous binary is already active"
  LOG_FIELDS="verb=rollback target=$target"

  record_previous "$current"
  switch_to "$target"
  restart_unit
  if check_health; then
    LOG_RESULT="rolled back"
    say "rolled back to $target; previous binary: $current"
    return
  fi

  [ -n "$current" ] || fail_unhealthy "rollback failed, nothing to restore"
  switch_to "$current"
  record_previous "$target"
  restart_unit
  if check_health; then
    fail_unhealthy "rollback failed, restored $current"
  fi
  fail_unhealthy "rollback failed, restored $current, which is not healthy either"
}

main() {
  [ -n "$COMMAND_GIVEN" ] || refuse "no command given; interactive sessions are not allowed"
  [ "${#RAW_COMMAND}" -le "$MAX_COMMAND_LENGTH" ] || refuse "command is longer than $MAX_COMMAND_LENGTH characters"
  [[ "$RAW_COMMAND" != *[!a-z0-9\ -]* ]] || refuse "command contains characters outside [a-z0-9 -]"
  local -a words
  read -r -a words <<< "$RAW_COMMAND"
  [ "${#words[@]}" -gt 0 ] || refuse "empty command"
  case "${words[0]}" in
    status)
      [ "${#words[@]}" -eq 1 ] || refuse "status takes no arguments"
      run_status
      ;;
    deploy)
      [ "${#words[@]}" -eq 4 ] || refuse "deploy takes exactly <version> <sha256> <commit>"
      run_deploy "${words[1]}" "${words[2]}" "${words[3]}"
      ;;
    rollback)
      [ "${#words[@]}" -eq 1 ] || refuse "rollback takes no arguments"
      run_rollback
      ;;
    *) refuse "unknown verb; allowed: status, deploy, rollback" ;;
  esac
}

trap finish EXIT
if [ -z "${HOME:-}" ] || [ ! -d "$ROOT" ]; then
  refuse "~/cliproxyapi is missing"
fi
exec 9>> "$LOCK_FILE"
LOG_READY=1
LOG_FIELDS="verb=refused command=\"$(sanitized_command)\""
flock -w "$LOCK_WAIT_SECONDS" 9 || refuse "another deploy-gate call still holds the lock"
main
