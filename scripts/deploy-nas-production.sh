#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  LOCAL_NAS_SHARED_PASSWORD=... bash scripts/deploy-nas-production.sh \
    --binary /path/to/m365-native \
    --sha256 <expected-sha256> \
    --commit <expected-git-commit>

Optional environment overrides:
  M365_NAS_SSH_TARGET       SSH target (default: home-nas-agent)
  M365_REMOTE_APP           Production binary path
  M365_REMOTE_DATA          Production data directory
  M365_REMOTE_COMPOSE       Production compose path
  M365_COMPOSE_PROJECT      Compose project (default: m365-copilot2api)
  M365_COMPOSE_SERVICE      Compose service (default: m365)
  M365_PUBLIC_HOST          Allowed Host header
  M365_LISTEN_PORT          Local production port (default: 14141)
  M365_READY_TIMEOUT        Readiness timeout in seconds (default: 90)
  M365_SMOKE_ENV            Remote env file containing M365_API_KEY; if absent,
                            authenticated models smoke tests are skipped.

The deployment creates a temporary rollback backup and removes it after a
successful deployment or a successful rollback. A backup is retained only if
rollback itself fails.
EOF
}

die() {
  printf 'deploy error: %s\n' "$*" >&2
  exit 1
}

binary=""
expected_sha=""
expected_commit=""
while (($#)); do
  case "$1" in
    --binary)
      [[ $# -ge 2 ]] || die "--binary requires a value"
      binary=$2
      shift 2
      ;;
    --sha256)
      [[ $# -ge 2 ]] || die "--sha256 requires a value"
      expected_sha=$2
      shift 2
      ;;
    --commit)
      [[ $# -ge 2 ]] || die "--commit requires a value"
      expected_commit=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ -n "$binary" && -f "$binary" ]] || die "--binary must point to a file"
[[ "$expected_sha" =~ ^[0-9a-fA-F]{64}$ ]] || die "--sha256 must be a 64-character hex digest"
[[ "$expected_commit" =~ ^[0-9a-fA-F]{7,40}$ ]] || die "--commit must be a Git commit id"
[[ -n "${LOCAL_NAS_SHARED_PASSWORD:-}" ]] || die "LOCAL_NAS_SHARED_PASSWORD is required"

if command -v sha256sum >/dev/null 2>&1; then
  actual_sha=$(sha256sum "$binary" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual_sha=$(shasum -a 256 "$binary" | awk '{print $1}')
else
  die "sha256sum or shasum is required"
fi
[[ "$actual_sha" == "$expected_sha" ]] || die "local binary SHA256 mismatch: got $actual_sha"

ssh_target=${M365_NAS_SSH_TARGET:-home-nas-agent}
remote_app=${M365_REMOTE_APP:-/volume1/docker/m365-copilot2api/app/m365-native}
remote_data=${M365_REMOTE_DATA:-/volume1/docker/m365-copilot2api/data}
remote_compose=${M365_REMOTE_COMPOSE:-/volume1/docker/m365-prod/compose.yaml}
compose_project=${M365_COMPOSE_PROJECT:-m365-copilot2api}
compose_service=${M365_COMPOSE_SERVICE:-m365}
public_host=${M365_PUBLIC_HOST:-m365.gabriel920.direct.quickconnect.to}
listen_port=${M365_LISTEN_PORT:-14141}
ready_timeout=${M365_READY_TIMEOUT:-90}
smoke_env=${M365_SMOKE_ENV:-/volume1/docker/hermes-memory/hindsight/.env}

[[ "$listen_port" =~ ^[0-9]+$ ]] || die "M365_LISTEN_PORT must be numeric"
[[ "$ready_timeout" =~ ^[0-9]+$ && "$ready_timeout" -ge 10 ]] || die "M365_READY_TIMEOUT must be at least 10 seconds"

remote_stage="/tmp/m365-native.deploy.$$.${expected_sha:0:12}"
staged=0
cleanup_remote_stage() {
  if ((staged)); then
    ssh -o BatchMode=yes "$ssh_target" "rm -f '$remote_stage'" >/dev/null 2>&1 || true
  fi
}
trap cleanup_remote_stage EXIT

printf 'staging binary to %s:%s\n' "$ssh_target" "$remote_stage"
ssh -o BatchMode=yes "$ssh_target" \
  "umask 077; dd of='$remote_stage' bs=1M status=none; chmod 700 '$remote_stage'; actual=\$(sha256sum '$remote_stage' | awk '{print \$1}'); test \"\$actual\" = '$expected_sha'; printf 'staged_sha256=%s\\n' \"\$actual\"" \
  < "$binary"
staged=1

remote_command="sudo -S -p '' /bin/bash -s -- '$remote_stage' '$expected_sha' '$expected_commit' '$remote_app' '$remote_data' '$remote_compose' '$compose_project' '$compose_service' '$public_host' '$listen_port' '$ready_timeout' '$smoke_env'"

{
  printf '%s\n' "$LOCAL_NAS_SHARED_PASSWORD"
  cat <<'REMOTE_SCRIPT'
set -Eeuo pipefail

stage=$1
expected_sha=$2
expected_commit=$3
app=$4
data=$5
compose=$6
project=$7
service=$8
public_host=$9
listen_port=${10}
ready_timeout=${11}
smoke_env=${12}

export PATH="/usr/local/bin:/volume1/@appstore/ContainerManager/usr/bin:/var/packages/ContainerManager/target/usr/bin:/usr/sbin:/usr/bin:/sbin:/bin"

docker_bin=""
for candidate in /usr/local/bin/docker /volume1/@appstore/ContainerManager/usr/bin/docker /var/packages/ContainerManager/target/usr/bin/docker; do
  if [[ -x "$candidate" ]]; then
    docker_bin=$candidate
    break
  fi
done
[[ -n "$docker_bin" ]] || { echo "docker binary unavailable" >&2; exit 1; }

compose_cmd=("$docker_bin" compose -p "$project" -f "$compose")
backup="$(dirname "$compose")/.deploy-backup-$(date +%Y%m%d-%H%M%S)-$$"
backup_ready=0
rollback_running=0

cleanup_stage() {
  rm -f "$stage" /tmp/m365-deploy-models.json /tmp/m365-deploy-memory-models.json
}
trap cleanup_stage EXIT

container_id() {
  "$docker_bin" ps -a \
    --filter "label=com.docker.compose.project=$project" \
    --filter "label=com.docker.compose.service=$service" \
    -q | head -n 1
}

wait_ready() {
  local deadline=$((SECONDS + ready_timeout))
  local cid status restarts http_code
  while ((SECONDS < deadline)); do
    cid=$(container_id)
    if [[ -n "$cid" ]]; then
      status=$("$docker_bin" inspect -f '{{.State.Status}}' "$cid" 2>/dev/null || true)
      restarts=$("$docker_bin" inspect -f '{{.RestartCount}}' "$cid" 2>/dev/null || true)
      http_code=$(curl -sS -m 3 -o /dev/null -w '%{http_code}' \
        -H "Host: $public_host" "http://127.0.0.1:$listen_port/" 2>/dev/null || true)
      if [[ "$status" == "running" && "$http_code" != "000" && "$http_code" =~ ^[1-4][0-9][0-9]$ ]]; then
        printf 'ready container=%s restart_count=%s http=%s\n' "$cid" "$restarts" "$http_code"
        return 0
      fi
    fi
    sleep 2
  done
  return 1
}

atomic_restore_file() {
  local source=$1 target=$2 tmp
  tmp="${target}.restore.$$"
  cp -p "$source" "$tmp"
  mv -f "$tmp" "$target"
}

rollback() {
  local original_rc=${1:-1}
  local rollback_ok=1
  ((rollback_running == 0)) || return "$original_rc"
  rollback_running=1
  trap - ERR
  set +e
  printf 'deployment failed; rolling back from %s\n' "$backup" >&2
  "${compose_cmd[@]}" stop "$service" >/dev/null 2>&1 || rollback_ok=0
  atomic_restore_file "$backup/m365-native" "$app" || rollback_ok=0
  atomic_restore_file "$backup/compose.yaml" "$compose" || rollback_ok=0
  atomic_restore_file "$backup/settings.json" "$data/settings.json" || rollback_ok=0
  "${compose_cmd[@]}" up -d --no-deps --force-recreate "$service" || rollback_ok=0
  if ((rollback_ok)) && wait_ready; then
    rm -rf "$backup"
    printf 'rollback succeeded; temporary backup removed\n' >&2
  else
    printf 'rollback incomplete; backup retained at %s\n' "$backup" >&2
  fi
  exit "$original_rc"
}

on_error() {
  local rc=$?
  if ((backup_ready)); then
    rollback "$rc"
  fi
  exit "$rc"
}
trap on_error ERR

[[ -f "$stage" ]] || { echo "staged binary missing" >&2; exit 1; }
[[ "$(sha256sum "$stage" | awk '{print $1}')" == "$expected_sha" ]] || { echo "staged SHA256 mismatch" >&2; exit 1; }
[[ -f "$app" && -f "$compose" && -f "$data/settings.json" ]] || { echo "production paths missing" >&2; exit 1; }

cid=$(container_id)
[[ -n "$cid" ]] || { echo "production container not found" >&2; exit 1; }
[[ "$("$docker_bin" inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$cid")" == "$project" ]] || exit 1
[[ "$("$docker_bin" inspect -f '{{index .Config.Labels "com.docker.compose.service"}}' "$cid")" == "$service" ]] || exit 1

mkdir -m 700 "$backup"
cp -p "$app" "$backup/m365-native"
cp -p "$compose" "$backup/compose.yaml"
cp -p "$data/settings.json" "$backup/settings.json"
[[ -s "$backup/m365-native" && -s "$backup/compose.yaml" && -s "$backup/settings.json" ]] || { echo "backup verification failed" >&2; exit 1; }
backup_ready=1
printf 'temporary_backup=%s\n' "$backup"

app_uid=$(stat -c %u "$app")
app_gid=$(stat -c %g "$app")
app_mode=$(stat -c %a "$app")
app_tmp="${app}.new.$$"
install -m "$app_mode" -o "$app_uid" -g "$app_gid" "$stage" "$app_tmp"
[[ "$(sha256sum "$app_tmp" | awk '{print $1}')" == "$expected_sha" ]]
mv -f "$app_tmp" "$app"

python3 - "$compose" <<'PY'
import os
import re
import tempfile
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as handle:
    source = handle.read()
updated = source
for name in ("M365_CHAT_TIMEOUT_SECONDS", "M365_IMAGE_TIMEOUT_SECONDS"):
    pattern = rf'(?m)^(\s*{re.escape(name)}:\s*)"?[0-9]+"?(\s*)$'
    updated, count = re.subn(pattern, rf'\g<1>"1800"\g<2>', updated)
    if count != 1:
        raise SystemExit(f"expected exactly one {name}, found {count}")
stat = os.stat(path)
fd, temp_path = tempfile.mkstemp(prefix=".compose-", dir=os.path.dirname(path))
try:
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        handle.write(updated)
        handle.flush()
        os.fsync(handle.fileno())
        os.fchmod(handle.fileno(), stat.st_mode & 0o7777)
        os.fchown(handle.fileno(), stat.st_uid, stat.st_gid)
    os.replace(temp_path, path)
    directory = os.open(os.path.dirname(path), os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)
except Exception:
    try:
        os.unlink(temp_path)
    except FileNotFoundError:
        pass
    raise
PY

"${compose_cmd[@]}" up -d --no-deps --force-recreate "$service"
wait_ready

[[ "$(sha256sum "$app" | awk '{print $1}')" == "$expected_sha" ]]
grep -aFq "$expected_commit" "$app"
grep -q 'M365_CHAT_TIMEOUT_SECONDS: "1800"' "$compose"
grep -q 'M365_IMAGE_TIMEOUT_SECONDS: "1800"' "$compose"

python3 - "$data/settings.json" <<'PY'
import json
import sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    settings = json.load(handle)
if settings.get("chatTimeoutSeconds") != 1800:
    raise SystemExit("chatTimeoutSeconds is not 1800")
if settings.get("imageTimeoutSeconds") != 1800:
    raise SystemExit("imageTimeoutSeconds is not 1800")
if settings.get("memoryCompatibilityEnabled") is not True:
    raise SystemExit("memoryCompatibilityEnabled is not true")
if "proxyPool" in settings:
    raise SystemExit("removed proxyPool is still persisted")
PY

if [[ -f "$smoke_env" ]]; then
  set -a
  # shellcheck disable=SC1090
  . "$smoke_env"
  set +a
  if [[ -n "${M365_API_KEY:-}" ]]; then
    for route in /v1/models /memory/v1/models; do
      code=$(curl -sS -m 12 \
        -H "Authorization: Bearer $M365_API_KEY" \
        -H "Host: $public_host" \
        -o /tmp/m365-deploy-models.json \
        -w '%{http_code}' \
        "http://127.0.0.1:$listen_port$route")
      [[ "$code" == "200" ]] || { echo "$route smoke failed with HTTP $code" >&2; exit 1; }
    done
    printf 'authenticated_models_smoke=pass\n'
  else
    printf 'authenticated_models_smoke=skipped_no_key\n'
  fi
else
  printf 'authenticated_models_smoke=skipped_no_env\n'
fi

cid=$(container_id)
status=$("$docker_bin" inspect -f '{{.State.Status}}' "$cid")
restarts=$("$docker_bin" inspect -f '{{.RestartCount}}' "$cid")
[[ "$status" == "running" ]]

rm -rf "$backup"
backup_ready=0
printf 'deployment=success\ncontainer=%s\nrestart_count=%s\nsha256=%s\nbackup_removed=true\n' "$cid" "$restarts" "$expected_sha"
REMOTE_SCRIPT
} | ssh -o BatchMode=yes "$ssh_target" "$remote_command"

cleanup_remote_stage
staged=0
trap - EXIT
