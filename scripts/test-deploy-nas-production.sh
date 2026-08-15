#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
deploy="$root/scripts/deploy-nas-production.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

run_bounded() {
  local ticks=$1 out=$2 err=$3
  shift 3
  "$@" >"$out" 2>"$err" &
  local pid=$! i rc
  for ((i = 0; i < ticks; i++)); do
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid"
      return $?
    fi
    sleep 0.1
  done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  return 124
}

mkdir -p "$tmp/bin" "$tmp/web"
commit=7f1e80cdf5732f6ab078fda4cef72fbe90fe878f
printf 'candidate-binary %s\n' "$commit" > "$tmp/m365-native"
printf '<html>index</html>\n' > "$tmp/web/index.html"
printf '<html>login</html>\n' > "$tmp/web/login.html"
sha=$(python3 - "$tmp/m365-native" <<'PY'
import hashlib
import sys
with open(sys.argv[1], "rb") as handle:
    print(hashlib.sha256(handle.read()).hexdigest())
PY
)
tree=d3987ddf92bb991b09fe9bc8c0809b3b57780522

cat > "$tmp/bin/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
count=0
if [[ -f "${SSH_COUNT_FILE:-}" ]]; then
  count=$(cat "$SSH_COUNT_FILE")
fi
count=$((count + 1))
printf '%s' "$count" > "$SSH_COUNT_FILE"
printf '%s\n' "$*" >> "$SSH_LOG"
case "$*" in
  *"dd of="*)
    remote_stage=$(sed -n "s/.*dd of='\([^']*\)'.*/\1/p" <<<"$*")
    [[ -n "$remote_stage" ]] || exit 96
    if [[ -n "${SSH_STAGE_CAPTURE:-}" ]]; then
      tee "$SSH_STAGE_CAPTURE" > "$remote_stage"
    else
      cat > "$remote_stage"
    fi
    if [[ "${SSH_CORRUPT_STAGE:-0}" == 1 ]]; then
      printf 'corrupt' >> "$remote_stage"
    fi
    ;;
  *"sudo -n /bin/bash -s"*)
    if [[ "${SSH_EXEC_REMOTE:-0}" != 1 ]]; then
      cat >/dev/null || true
    fi
    ;;
esac
if [[ $count -eq ${SSH_FAIL_ON_CALL:-1} ]]; then
  exit "${SSH_FAIL_CODE:-99}"
fi
if [[ "${SSH_EXEC_REMOTE:-0}" == 1 && "$*" == *"sudo -n /bin/bash -s"* ]]; then
  args=("$@")
  marker=-1
  for i in "${!args[@]}"; do
    if [[ "${args[$i]}" == "--" ]]; then
      marker=$i
      break
    fi
  done
  [[ $marker -ge 0 ]] || exit 98
  bash -s -- "${args[@]:marker+1}"
elif [[ "$*" == *" rm -f -- /tmp/m365-release.deploy."* ]]; then
  rm -f -- "${@: -1}"
fi
exit 0
EOF
chmod +x "$tmp/bin/ssh"

cat > "$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$DOCKER_LOG"
case "${1:-}" in
  ps)
    echo test-container
    ;;
  inspect)
    case "$*" in
      *RestartCount*) echo 0 ;;
      *State.Status*) echo running ;;
      *com.docker.compose.project*) echo m365-copilot2api ;;
      *com.docker.compose.service*) echo m365 ;;
      *) echo unknown ;;
    esac
    ;;
  compose)
    if [[ "$*" == *" up "* && ( "${DOCKER_FAIL_FIRST_UP:-0}" == 1 || -n "${DOCKER_CORRUPT_ON_SECOND_UP:-}" ) ]]; then
      [[ -n "${DOCKER_UP_COUNT_FILE:-}" ]] || exit 97
      up_count=0
      if [[ -f "$DOCKER_UP_COUNT_FILE" ]]; then
        up_count=$(cat "$DOCKER_UP_COUNT_FILE")
      fi
      up_count=$((up_count + 1))
      printf '%s' "$up_count" > "$DOCKER_UP_COUNT_FILE"
      if [[ $up_count -eq 1 && "${DOCKER_FAIL_FIRST_UP:-0}" == 1 ]]; then
        exit 42
      fi
      if [[ $up_count -eq 2 && -n "${DOCKER_CORRUPT_ON_SECOND_UP:-}" ]]; then
        printf 'corrupted-after-restore\n' > "$DOCKER_CORRUPT_ON_SECOND_UP"
      fi
    fi
    exit 0
    ;;
esac
EOF
chmod +x "$tmp/bin/docker"

cat > "$tmp/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
out=""
while (($#)); do
  if [[ "$1" == "-o" && $# -ge 2 ]]; then
    out=$2
    shift 2
    continue
  fi
  shift
done
[[ -z "$out" ]] || printf '{}\n' > "$out"
printf '200'
EOF
chmod +x "$tmp/bin/curl"

err="$tmp/missing-web.err"
export SSH_COUNT_FILE="$tmp/ssh-count"
export SSH_LOG="$tmp/ssh.log"
export SSH_FAIL_ON_CALL=1
export SSH_FAIL_CODE=99
set +e
PATH="$tmp/bin:$PATH" bash "$deploy" \
  --binary "$tmp/m365-native" \
  --web-dir "$tmp/web" \
  --sha256 "$sha" \
  --commit "$commit" \
  --tree "$tree" \
  >"$tmp/missing-web.out" 2>"$err"
rc=$?
set -e

if [[ $rc -eq 0 ]]; then
  echo "FAIL: deployment accepted a release unit missing web/debug.html" >&2
  exit 1
fi
if ! grep -Fq 'web/debug.html' "$err"; then
  echo "FAIL: missing web/debug.html was not reported" >&2
  cat "$err" >&2
  exit 1
fi
if [[ -s "$SSH_LOG" ]]; then
  echo "FAIL: missing web asset reached SSH" >&2
  exit 1
fi

echo "PASS: missing web asset fails closed before SSH"

printf '<html>linked-debug</html>\n' > "$tmp/linked-debug.html"
ln -s "$tmp/linked-debug.html" "$tmp/web/debug.html"
: > "$SSH_LOG"
rm -f "$SSH_COUNT_FILE"
export SSH_FAIL_ON_CALL=1
err="$tmp/symlink-web.err"
set +e
PATH="$tmp/bin:$PATH" bash "$deploy" \
  --binary "$tmp/m365-native" \
  --web-dir "$tmp/web" \
  --sha256 "$sha" \
  --commit "$commit" \
  --tree "$tree" \
  >"$tmp/symlink-web.out" 2>"$err"
rc=$?
set -e
if [[ $rc -eq 0 ]]; then
  echo "FAIL: deployment accepted symlinked web/debug.html" >&2
  exit 1
fi
if ! grep -Fq 'regular file' "$err"; then
  echo "FAIL: symlinked web asset was not rejected as non-regular input" >&2
  cat "$err" >&2
  exit 1
fi
if [[ -s "$SSH_LOG" ]]; then
  echo "FAIL: symlinked web asset reached SSH" >&2
  exit 1
fi
rm -f "$tmp/web/debug.html"

echo "PASS: symlinked web asset fails closed before SSH"

printf '<html>debug</html>\n' > "$tmp/web/debug.html"

mkdir -p "$tmp/localtmp"
: > "$SSH_LOG"
rm -f "$SSH_COUNT_FILE"
unset SSH_STAGE_CAPTURE SSH_EXEC_REMOTE SSH_CORRUPT_STAGE
export SSH_FAIL_ON_CALL=1
export SSH_FAIL_CODE=99
set +e
run_bounded 50 "$tmp/stage-failure.out" "$tmp/stage-failure.err" \
  env TMPDIR="$tmp/localtmp" PATH="$tmp/bin:$PATH" bash "$deploy" \
    --binary "$tmp/m365-native" \
    --web-dir "$tmp/web" \
    --sha256 "$sha" \
    --commit "$commit" \
    --tree "$tree"
rc=$?
set -e
if [[ $rc -eq 124 ]]; then
  echo "FAIL: first-stage failure cleanup hung past 5 seconds" >&2
  exit 1
fi
if [[ $rc -ne 99 ]]; then
  echo "FAIL: first-stage failure did not preserve SSH rc=99 (rc=$rc)" >&2
  cat "$tmp/stage-failure.err" >&2
  exit 1
fi
remote_partial=$(sed -n "s/.*dd of='\([^']*\)'.*/\1/p" "$SSH_LOG" | head -n 1)
if [[ -z "$remote_partial" ]]; then
  echo "FAIL: first-stage failure did not create the expected remote partial path" >&2
  exit 1
fi
if [[ -e "$remote_partial" ]]; then
  echo "FAIL: first-stage failure left partial remote release $remote_partial" >&2
  exit 1
fi
if find "$tmp/localtmp" -maxdepth 1 -type f -name 'm365-production-release.*' | grep -q .; then
  echo "FAIL: first-stage failure left local release archive behind" >&2
  exit 1
fi

echo "PASS: first-stage failure cleanup is bounded and removes partial release artifacts"

: > "$SSH_LOG"
rm -f "$SSH_COUNT_FILE"
export SSH_FAIL_ON_CALL=2
export SSH_FAIL_CODE=77
export SSH_STAGE_CAPTURE="$tmp/staged-release.tar"
err="$tmp/sudo-n.err"
set +e
env -u LOCAL_NAS_SHARED_PASSWORD PATH="$tmp/bin:$PATH" bash "$deploy" \
  --binary "$tmp/m365-native" \
  --web-dir "$tmp/web" \
  --sha256 "$sha" \
  --commit "$commit" \
  --tree "$tree" \
  >"$tmp/sudo-n.out" 2>"$err"
rc=$?
set -e

if [[ $rc -ne 77 ]]; then
  echo "FAIL: valid release unit did not reach second SSH without shared password (rc=$rc)" >&2
  cat "$err" >&2
  exit 1
fi
sudo_line=$(grep -F "sudo " "$SSH_LOG" | tail -n 1)
if ! grep -Fq "sudo -n" <<<"$sudo_line"; then
  echo "FAIL: remote deployment does not use sudo -n" >&2
  cat "$SSH_LOG" >&2
  exit 1
fi
if grep -Fq "sudo -S" <<<"$sudo_line"; then
  echo "FAIL: remote deployment still uses password-fed sudo -S" >&2
  exit 1
fi

echo "PASS: deployment uses non-interactive sudo -n without shared password"

if ! tar -tf "$SSH_STAGE_CAPTURE" > "$tmp/stage-list" 2>/dev/null; then
  echo "FAIL: first SSH did not stage a release archive" >&2
  exit 1
fi
cat > "$tmp/expected-stage-list" <<'EOF'
manifest.json
m365-native
web/index.html
web/login.html
web/debug.html
EOF
if ! cmp -s "$tmp/expected-stage-list" "$tmp/stage-list"; then
  echo "FAIL: staged release archive has unexpected contents" >&2
  cat "$tmp/stage-list" >&2
  exit 1
fi
manifest=$(tar -xOf "$SSH_STAGE_CAPTURE" manifest.json)
for expected in \
  '"commit":"7f1e80cdf5732f6ab078fda4cef72fbe90fe878f"' \
  '"tree":"d3987ddf92bb991b09fe9bc8c0809b3b57780522"' \
  '"m365-native"' \
  '"web/index.html"' \
  '"web/login.html"' \
  '"web/debug.html"'; do
  if [[ "$manifest" != *"$expected"* ]]; then
    echo "FAIL: release manifest missing $expected" >&2
    exit 1
  fi
done

echo "PASS: staged release archive binds commit tree binary and web assets"

remote="$tmp/remote"
mkdir -p "$remote/app/web" "$remote/data"
printf 'old-binary\n' > "$remote/app/m365-native"
chmod 755 "$remote/app/m365-native"
printf 'old-index\n' > "$remote/app/web/index.html"
printf 'old-login\n' > "$remote/app/web/login.html"
printf 'old-debug\n' > "$remote/app/web/debug.html"
cat > "$remote/compose.yaml" <<'EOF'
services:
  m365:
    environment:
      M365_CHAT_TIMEOUT_SECONDS: "1800"
      M365_IMAGE_TIMEOUT_SECONDS: "1800"
EOF
cat > "$remote/data/settings.json" <<'EOF'
{"chatTimeoutSeconds":1800,"imageTimeoutSeconds":1800,"memoryCompatibilityEnabled":true}
EOF

: > "$SSH_LOG"
rm -f "$SSH_COUNT_FILE"
export SSH_FAIL_ON_CALL=999
export SSH_EXEC_REMOTE=1
export DOCKER_LOG="$tmp/docker.log"
export SSH_STAGE_CAPTURE="$tmp/success-release.tar"
set +e
PATH="$tmp/bin:$PATH" \
M365_REMOTE_APP="$remote/app/m365-native" \
M365_REMOTE_DATA="$remote/data" \
M365_REMOTE_COMPOSE="$remote/compose.yaml" \
M365_SMOKE_ENV="$remote/missing-smoke.env" \
M365_READY_TIMEOUT=10 \
bash "$deploy" \
  --binary "$tmp/m365-native" \
  --web-dir "$tmp/web" \
  --sha256 "$sha" \
  --commit "$commit" \
  --tree "$tree" \
  >"$tmp/success.out" 2>"$tmp/success.err"
rc=$?
set -e
if [[ $rc -ne 0 ]]; then
  echo "FAIL: isolated release-unit deployment did not succeed (rc=$rc)" >&2
  cat "$tmp/success.err" >&2
  exit 1
fi
cmp -s "$tmp/m365-native" "$remote/app/m365-native" || { echo "FAIL: binary not deployed" >&2; exit 1; }
for asset in index.html login.html debug.html; do
  cmp -s "$tmp/web/$asset" "$remote/app/web/$asset" || {
    echo "FAIL: web/$asset not deployed with binary" >&2
    exit 1
  }
done

echo "PASS: isolated deployment switches binary and all web assets together"

: > "$SSH_LOG"
rm -f "$SSH_COUNT_FILE"
export SSH_FAIL_ON_CALL=2
export SSH_FAIL_CODE=77
export SSH_EXEC_REMOTE=0
export SSH_CORRUPT_STAGE=0
export SSH_STAGE_CAPTURE="$tmp/deterministic-release.tar"
set +e
PATH="$tmp/bin:$PATH" bash "$deploy" \
  --binary "$tmp/m365-native" \
  --web-dir "$tmp/web" \
  --sha256 "$sha" \
  --commit "$commit" \
  --tree "$tree" \
  >"$tmp/deterministic.out" 2>"$tmp/deterministic.err"
rc=$?
set -e
[[ $rc -eq 77 ]] || { echo "FAIL: deterministic archive probe did not reach second SSH" >&2; exit 1; }
cmp -s "$tmp/success-release.tar" "$tmp/deterministic-release.tar" || {
  echo "FAIL: identical release inputs produced different archive bytes" >&2
  exit 1
}

echo "PASS: identical release inputs produce deterministic archive bytes"

printf 'pre-sha-binary\n' > "$remote/app/m365-native"
chmod 755 "$remote/app/m365-native"
printf 'pre-sha-index\n' > "$remote/app/web/index.html"
printf 'pre-sha-login\n' > "$remote/app/web/login.html"
printf 'pre-sha-debug\n' > "$remote/app/web/debug.html"
cp "$remote/app/m365-native" "$tmp/pre-sha-binary"
cp "$remote/app/web/index.html" "$tmp/pre-sha-index"
cp "$remote/app/web/login.html" "$tmp/pre-sha-login"
cp "$remote/app/web/debug.html" "$tmp/pre-sha-debug"
: > "$SSH_LOG"
rm -f "$SSH_COUNT_FILE"
export SSH_FAIL_ON_CALL=999
export SSH_EXEC_REMOTE=1
export SSH_CORRUPT_STAGE=1
export SSH_STAGE_CAPTURE="$tmp/corrupt-release.tar"
set +e
PATH="$tmp/bin:$PATH" \
M365_REMOTE_APP="$remote/app/m365-native" \
M365_REMOTE_DATA="$remote/data" \
M365_REMOTE_COMPOSE="$remote/compose.yaml" \
M365_SMOKE_ENV="$remote/missing-smoke.env" \
M365_READY_TIMEOUT=10 \
bash "$deploy" \
  --binary "$tmp/m365-native" \
  --web-dir "$tmp/web" \
  --sha256 "$sha" \
  --commit "$commit" \
  --tree "$tree" \
  >"$tmp/corrupt.out" 2>"$tmp/corrupt.err"
rc=$?
set -e
if [[ $rc -eq 0 || ! -s "$tmp/corrupt.err" ]] || ! grep -Fq 'staged release SHA256 mismatch' "$tmp/corrupt.err"; then
  echo "FAIL: corrupted staged release did not fail closed on SHA mismatch" >&2
  cat "$tmp/corrupt.err" >&2
  exit 1
fi
cmp -s "$tmp/pre-sha-binary" "$remote/app/m365-native" || { echo "FAIL: SHA mismatch changed binary" >&2; exit 1; }
cmp -s "$tmp/pre-sha-index" "$remote/app/web/index.html" || { echo "FAIL: SHA mismatch changed index" >&2; exit 1; }
cmp -s "$tmp/pre-sha-login" "$remote/app/web/login.html" || { echo "FAIL: SHA mismatch changed login" >&2; exit 1; }
cmp -s "$tmp/pre-sha-debug" "$remote/app/web/debug.html" || { echo "FAIL: SHA mismatch changed debug" >&2; exit 1; }

echo "PASS: staged release SHA mismatch fails closed before Production mutation"

export SSH_CORRUPT_STAGE=0
printf 'rollback-binary\n' > "$remote/app/m365-native"
chmod 755 "$remote/app/m365-native"
printf 'rollback-index\n' > "$remote/app/web/index.html"
printf 'rollback-login\n' > "$remote/app/web/login.html"
printf 'rollback-debug\n' > "$remote/app/web/debug.html"
cat > "$remote/compose.yaml" <<'EOF'
services:
  m365:
    environment:
      M365_CHAT_TIMEOUT_SECONDS: "111"
      M365_IMAGE_TIMEOUT_SECONDS: "222"
EOF
cp "$remote/app/m365-native" "$tmp/rollback-binary"
cp "$remote/app/web/index.html" "$tmp/rollback-index"
cp "$remote/app/web/login.html" "$tmp/rollback-login"
cp "$remote/app/web/debug.html" "$tmp/rollback-debug"
cp "$remote/compose.yaml" "$tmp/rollback-compose"
cp "$remote/data/settings.json" "$tmp/rollback-settings"
: > "$SSH_LOG"
rm -f "$SSH_COUNT_FILE" "$tmp/docker-up-count"
export SSH_FAIL_ON_CALL=999
export SSH_EXEC_REMOTE=1
export DOCKER_FAIL_FIRST_UP=1
export DOCKER_UP_COUNT_FILE="$tmp/docker-up-count"
export SSH_STAGE_CAPTURE="$tmp/rollback-release.tar"
set +e
PATH="$tmp/bin:$PATH" \
M365_REMOTE_APP="$remote/app/m365-native" \
M365_REMOTE_DATA="$remote/data" \
M365_REMOTE_COMPOSE="$remote/compose.yaml" \
M365_SMOKE_ENV="$remote/missing-smoke.env" \
M365_READY_TIMEOUT=10 \
bash "$deploy" \
  --binary "$tmp/m365-native" \
  --web-dir "$tmp/web" \
  --sha256 "$sha" \
  --commit "$commit" \
  --tree "$tree" \
  >"$tmp/rollback.out" 2>"$tmp/rollback.err"
rc=$?
set -e
if [[ $rc -ne 42 ]]; then
  echo "FAIL: injected deployment failure did not return original rc=42 (rc=$rc)" >&2
  cat "$tmp/rollback.err" >&2
  exit 1
fi
grep -Fq 'rollback succeeded' "$tmp/rollback.err" || { echo "FAIL: rollback did not report success" >&2; cat "$tmp/rollback.err" >&2; exit 1; }
cmp -s "$tmp/rollback-binary" "$remote/app/m365-native" || { echo "FAIL: rollback did not restore binary" >&2; exit 1; }
cmp -s "$tmp/rollback-index" "$remote/app/web/index.html" || { echo "FAIL: rollback did not restore index" >&2; exit 1; }
cmp -s "$tmp/rollback-login" "$remote/app/web/login.html" || { echo "FAIL: rollback did not restore login" >&2; exit 1; }
cmp -s "$tmp/rollback-debug" "$remote/app/web/debug.html" || { echo "FAIL: rollback did not restore debug" >&2; exit 1; }
cmp -s "$tmp/rollback-compose" "$remote/compose.yaml" || { echo "FAIL: rollback did not restore compose" >&2; exit 1; }
cmp -s "$tmp/rollback-settings" "$remote/data/settings.json" || { echo "FAIL: rollback did not restore settings" >&2; exit 1; }
if find "$remote" -maxdepth 1 -type d -name '.deploy-backup-*' | grep -q .; then
  echo "FAIL: successful rollback left backup directory behind" >&2
  exit 1
fi

echo "PASS: injected failure rolls back binary web compose and settings together"

rm -rf "$remote"/.deploy-backup-*
printf 'verify-binary\n' > "$remote/app/m365-native"
chmod 755 "$remote/app/m365-native"
printf 'verify-index\n' > "$remote/app/web/index.html"
printf 'verify-login\n' > "$remote/app/web/login.html"
printf 'verify-debug\n' > "$remote/app/web/debug.html"
cat > "$remote/compose.yaml" <<'EOF'
services:
  m365:
    environment:
      M365_CHAT_TIMEOUT_SECONDS: "333"
      M365_IMAGE_TIMEOUT_SECONDS: "444"
EOF
: > "$SSH_LOG"
rm -f "$SSH_COUNT_FILE" "$tmp/docker-up-count"
export SSH_FAIL_ON_CALL=999
export SSH_EXEC_REMOTE=1
export DOCKER_FAIL_FIRST_UP=1
export DOCKER_UP_COUNT_FILE="$tmp/docker-up-count"
export DOCKER_CORRUPT_ON_SECOND_UP="$remote/app/web/index.html"
export SSH_STAGE_CAPTURE="$tmp/rollback-verify-release.tar"
set +e
PATH="$tmp/bin:$PATH" \
M365_REMOTE_APP="$remote/app/m365-native" \
M365_REMOTE_DATA="$remote/data" \
M365_REMOTE_COMPOSE="$remote/compose.yaml" \
M365_SMOKE_ENV="$remote/missing-smoke.env" \
M365_READY_TIMEOUT=10 \
bash "$deploy" \
  --binary "$tmp/m365-native" \
  --web-dir "$tmp/web" \
  --sha256 "$sha" \
  --commit "$commit" \
  --tree "$tree" \
  >"$tmp/rollback-verify.out" 2>"$tmp/rollback-verify.err"
rc=$?
set -e
unset DOCKER_CORRUPT_ON_SECOND_UP
if [[ $rc -ne 42 ]]; then
  echo "FAIL: rollback identity probe did not preserve original rc=42 (rc=$rc)" >&2
  cat "$tmp/rollback-verify.err" >&2
  exit 1
fi
if ! grep -Fq 'rollback incomplete; backup retained' "$tmp/rollback-verify.err"; then
  echo "FAIL: rollback reported success without detecting restored-file drift" >&2
  cat "$tmp/rollback-verify.err" >&2
  exit 1
fi
if ! find "$remote" -maxdepth 1 -type d -name '.deploy-backup-*' | grep -q .; then
  echo "FAIL: rollback identity mismatch did not retain recovery backup" >&2
  exit 1
fi

echo "PASS: rollback success requires independent restored-file identity readback"
