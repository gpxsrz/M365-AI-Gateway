#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash scripts/deploy-nas-production.sh \
    --binary /path/to/m365-native \
    --web-dir /path/to/web \
    --sha256 <expected-sha256> \
    --commit <expected-git-commit> \
    --tree <expected-git-tree>

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
web_dir=""
expected_sha=""
expected_commit=""
expected_tree=""
while (($#)); do
  case "$1" in
    --binary)
      [[ $# -ge 2 ]] || die "--binary requires a value"
      binary=$2
      shift 2
      ;;
    --web-dir)
      [[ $# -ge 2 ]] || die "--web-dir requires a value"
      web_dir=$2
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
    --tree)
      [[ $# -ge 2 ]] || die "--tree requires a value"
      expected_tree=$2
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

[[ -n "$binary" && -f "$binary" && ! -L "$binary" ]] || die "--binary must point to a regular file"
[[ -n "$web_dir" && -d "$web_dir" && ! -L "$web_dir" ]] || die "--web-dir must point to a real directory"
for asset in index.html login.html debug.html; do
  [[ -f "$web_dir/$asset" && ! -L "$web_dir/$asset" ]] || die "web/$asset must be a regular file"
done
[[ "$expected_sha" =~ ^[0-9a-fA-F]{64}$ ]] || die "--sha256 must be a 64-character hex digest"
[[ "$expected_commit" =~ ^[0-9a-fA-F]{40}$ ]] || die "--commit must be a 40-character Git commit id"
[[ "$expected_tree" =~ ^[0-9a-fA-F]{40}$ ]] || die "--tree must be a 40-character Git tree id"

command -v python3 >/dev/null 2>&1 || die "python3 is required"
local_release=$(mktemp "${TMPDIR:-/tmp}/m365-production-release.XXXXXX")
release_meta=$(python3 - "$binary" "$web_dir" "$expected_sha" "$expected_commit" "$expected_tree" "$local_release" <<'PY'
import hashlib
import io
import json
import os
import sys
import tarfile

binary, web_dir, expected_binary_sha, commit, tree, output = sys.argv[1:]
files = [
    ("m365-native", binary, 0o755),
    ("web/index.html", os.path.join(web_dir, "index.html"), 0o644),
    ("web/login.html", os.path.join(web_dir, "login.html"), 0o644),
    ("web/debug.html", os.path.join(web_dir, "debug.html"), 0o644),
]

payloads = {}
digests = {}
for name, path, _ in files:
    with open(path, "rb") as handle:
        payloads[name] = handle.read()
    digests[name] = hashlib.sha256(payloads[name]).hexdigest()

if digests["m365-native"] != expected_binary_sha.lower():
    raise SystemExit(f"local binary SHA256 mismatch: got {digests['m365-native']}")

manifest = {
    "schema": 1,
    "commit": commit.lower(),
    "tree": tree.lower(),
    "files": {name: {"sha256": digests[name]} for name, _, _ in files},
}
manifest_bytes = (json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n").encode()

with tarfile.open(output, "w", format=tarfile.USTAR_FORMAT) as archive:
    entries = [("manifest.json", manifest_bytes, 0o644)] + [
        (name, payloads[name], mode) for name, _, mode in files
    ]
    for name, data, mode in entries:
        info = tarfile.TarInfo(name)
        info.size = len(data)
        info.mode = mode
        info.uid = 0
        info.gid = 0
        info.uname = ""
        info.gname = ""
        info.mtime = 0
        archive.addfile(info, io.BytesIO(data))

with open(output, "rb") as handle:
    release_sha = hashlib.sha256(handle.read()).hexdigest()
print("\t".join([
    release_sha,
    digests["web/index.html"],
    digests["web/login.html"],
    digests["web/debug.html"],
]))
PY
) || die "failed to build deterministic release archive"
IFS=$'\t' read -r release_sha index_sha login_sha debug_sha <<<"$release_meta"

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

remote_stage="/tmp/m365-release.deploy.$$.${release_sha:0:12}.tar"
remote_stage_may_exist=0
cleanup_remote_stage() {
  rm -f "$local_release"
  if ((remote_stage_may_exist)); then
    ssh -o BatchMode=yes -o ConnectTimeout=5 -o ServerAliveInterval=5 -o ServerAliveCountMax=1 \
      "$ssh_target" rm -f -- "$remote_stage" >/dev/null 2>&1 || true
  fi
}
trap cleanup_remote_stage EXIT

printf 'staging release to %s:%s\n' "$ssh_target" "$remote_stage"
remote_stage_may_exist=1
ssh -o BatchMode=yes "$ssh_target" \
  "umask 077; dd of='$remote_stage' bs=1M status=none; chmod 600 '$remote_stage'; actual=\$(sha256sum '$remote_stage' | awk '{print \$1}'); test \"\$actual\" = '$release_sha'; printf 'staged_release_sha256=%s\\n' \"\$actual\"" \
  < "$local_release"

cat <<'REMOTE_SCRIPT' | ssh -o BatchMode=yes "$ssh_target" sudo -n /bin/bash -s -- \
  "$remote_stage" "$release_sha" "$expected_sha" "$expected_commit" "$expected_tree" \
  "$index_sha" "$login_sha" "$debug_sha" "$remote_app" "$remote_data" "$remote_compose" \
  "$compose_project" "$compose_service" "$public_host" "$listen_port" "$ready_timeout" "$smoke_env"
set -Eeuo pipefail

stage=$1
release_sha=$2
expected_sha=$3
expected_commit=$4
expected_tree=$5
index_sha=$6
login_sha=$7
debug_sha=$8
app=$9
data=${10}
compose=${11}
project=${12}
service=${13}
public_host=${14}
listen_port=${15}
ready_timeout=${16}
smoke_env=${17}

export PATH="${PATH:-/usr/bin:/bin}:/usr/local/bin:/volume1/@appstore/ContainerManager/usr/bin:/var/packages/ContainerManager/target/usr/bin:/usr/sbin:/usr/bin:/sbin:/bin"

docker_bin=$(command -v docker 2>/dev/null || true)
[[ -n "$docker_bin" ]] || { echo "docker binary unavailable" >&2; exit 1; }

compose_cmd=("$docker_bin" compose -p "$project" -f "$compose")
backup="$(dirname "$compose")/.deploy-backup-$(date +%Y%m%d-%H%M%S)-$$"
stage_dir="${stage%.tar}.dir"
web_dir="$(dirname "$app")/web"
backup_ready=0
rollback_running=0

sha256_file() {
  python3 - "$1" <<'PY'
import hashlib
import sys
with open(sys.argv[1], "rb") as handle:
    print(hashlib.sha256(handle.read()).hexdigest())
PY
}

cleanup_stage() {
  rm -rf "$stage" "$stage_dir" /tmp/m365-deploy-models.json /tmp/m365-deploy-memory-models.json
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
  atomic_restore_file "$backup/index.html" "$web_dir/index.html" || rollback_ok=0
  atomic_restore_file "$backup/login.html" "$web_dir/login.html" || rollback_ok=0
  atomic_restore_file "$backup/debug.html" "$web_dir/debug.html" || rollback_ok=0
  atomic_restore_file "$backup/compose.yaml" "$compose" || rollback_ok=0
  atomic_restore_file "$backup/settings.json" "$data/settings.json" || rollback_ok=0
  "${compose_cmd[@]}" up -d --no-deps --force-recreate "$service" || rollback_ok=0
  if ((rollback_ok)) && ! wait_ready; then
    rollback_ok=0
  fi
  if ((rollback_ok)); then
    [[ "$(sha256_file "$backup/m365-native")" == "$(sha256_file "$app")" ]] || rollback_ok=0
    [[ "$(sha256_file "$backup/index.html")" == "$(sha256_file "$web_dir/index.html")" ]] || rollback_ok=0
    [[ "$(sha256_file "$backup/login.html")" == "$(sha256_file "$web_dir/login.html")" ]] || rollback_ok=0
    [[ "$(sha256_file "$backup/debug.html")" == "$(sha256_file "$web_dir/debug.html")" ]] || rollback_ok=0
    [[ "$(sha256_file "$backup/compose.yaml")" == "$(sha256_file "$compose")" ]] || rollback_ok=0
    [[ "$(sha256_file "$backup/settings.json")" == "$(sha256_file "$data/settings.json")" ]] || rollback_ok=0
  fi
  if ((rollback_ok)); then
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

[[ -f "$stage" ]] || { echo "staged release missing" >&2; exit 1; }
[[ "$(sha256_file "$stage")" == "$release_sha" ]] || { echo "staged release SHA256 mismatch" >&2; exit 1; }
rm -rf "$stage_dir"
mkdir -m 700 "$stage_dir"
python3 - "$stage" "$stage_dir" "$expected_sha" "$expected_commit" "$expected_tree" "$index_sha" "$login_sha" "$debug_sha" <<'PY'
import hashlib
import json
import os
import sys
import tarfile

archive_path, stage_dir, binary_sha, commit, tree, index_sha, login_sha, debug_sha = sys.argv[1:]
expected = {
    "m365-native": binary_sha,
    "web/index.html": index_sha,
    "web/login.html": login_sha,
    "web/debug.html": debug_sha,
}
expected_names = ["manifest.json", *expected]
with tarfile.open(archive_path, "r:") as archive:
    members = archive.getmembers()
    names = [member.name for member in members]
    if names != expected_names:
        raise SystemExit(f"unexpected release archive members: {names}")
    if any(not member.isfile() for member in members):
        raise SystemExit("release archive contains a non-regular member")
    manifest = json.loads(archive.extractfile("manifest.json").read())
    if manifest.get("schema") != 1 or manifest.get("commit") != commit.lower() or manifest.get("tree") != tree.lower():
        raise SystemExit("release manifest identity mismatch")
    manifest_files = manifest.get("files") or {}
    for name, digest in expected.items():
        if (manifest_files.get(name) or {}).get("sha256") != digest:
            raise SystemExit(f"release manifest SHA mismatch for {name}")
        data = archive.extractfile(name).read()
        actual = hashlib.sha256(data).hexdigest()
        if actual != digest:
            raise SystemExit(f"release payload SHA mismatch for {name}")
        target = os.path.join(stage_dir, *name.split("/"))
        os.makedirs(os.path.dirname(target), mode=0o700, exist_ok=True)
        with open(target, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(target, 0o755 if name == "m365-native" else 0o644)
PY

for path in "$app" "$web_dir/index.html" "$web_dir/login.html" "$web_dir/debug.html" "$compose" "$data/settings.json"; do
  [[ -f "$path" ]] || { echo "production path missing: $path" >&2; exit 1; }
done

cid=$(container_id)
[[ -n "$cid" ]] || { echo "production container not found" >&2; exit 1; }
[[ "$("$docker_bin" inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$cid")" == "$project" ]] || exit 1
[[ "$("$docker_bin" inspect -f '{{index .Config.Labels "com.docker.compose.service"}}' "$cid")" == "$service" ]] || exit 1

mkdir -m 700 "$backup"
cp -p "$app" "$backup/m365-native"
cp -p "$web_dir/index.html" "$backup/index.html"
cp -p "$web_dir/login.html" "$backup/login.html"
cp -p "$web_dir/debug.html" "$backup/debug.html"
cp -p "$compose" "$backup/compose.yaml"
cp -p "$data/settings.json" "$backup/settings.json"
for path in m365-native index.html login.html debug.html compose.yaml settings.json; do
  [[ -s "$backup/$path" ]] || { echo "backup verification failed: $path" >&2; exit 1; }
done
backup_ready=1
printf 'temporary_backup=%s\n' "$backup"

atomic_install_file() {
  local source=$1 target=$2 expected=$3
  python3 - "$source" "$target" "$expected" <<'PY'
import hashlib
import os
import sys
import tempfile

source, target, expected = sys.argv[1:]
with open(source, "rb") as handle:
    data = handle.read()
actual = hashlib.sha256(data).hexdigest()
if actual != expected:
    raise SystemExit(f"staged file SHA mismatch for {target}: {actual}")
stat = os.stat(target, follow_symlinks=False)
fd, temp_path = tempfile.mkstemp(prefix=os.path.basename(target) + ".new-", dir=os.path.dirname(target))
try:
    with os.fdopen(fd, "wb") as handle:
        handle.write(data)
        handle.flush()
        os.fsync(handle.fileno())
        os.fchmod(handle.fileno(), stat.st_mode & 0o7777)
        try:
            os.fchown(handle.fileno(), stat.st_uid, stat.st_gid)
        except PermissionError:
            if os.geteuid() == 0:
                raise
    os.replace(temp_path, target)
    directory = os.open(os.path.dirname(target), os.O_RDONLY)
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
}

"${compose_cmd[@]}" stop "$service"
atomic_install_file "$stage_dir/m365-native" "$app" "$expected_sha"
atomic_install_file "$stage_dir/web/index.html" "$web_dir/index.html" "$index_sha"
atomic_install_file "$stage_dir/web/login.html" "$web_dir/login.html" "$login_sha"
atomic_install_file "$stage_dir/web/debug.html" "$web_dir/debug.html" "$debug_sha"

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

[[ "$(sha256_file "$app")" == "$expected_sha" ]]
[[ "$(sha256_file "$web_dir/index.html")" == "$index_sha" ]]
[[ "$(sha256_file "$web_dir/login.html")" == "$login_sha" ]]
[[ "$(sha256_file "$web_dir/debug.html")" == "$debug_sha" ]]
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
printf 'deployment=success\ncontainer=%s\nrestart_count=%s\ncommit=%s\ntree=%s\nrelease_sha256=%s\nbinary_sha256=%s\nindex_sha256=%s\nlogin_sha256=%s\ndebug_sha256=%s\nbackup_removed=true\n' \
  "$cid" "$restarts" "$expected_commit" "$expected_tree" "$release_sha" "$expected_sha" "$index_sha" "$login_sha" "$debug_sha"
REMOTE_SCRIPT

cleanup_remote_stage
remote_stage_may_exist=0
trap - EXIT
