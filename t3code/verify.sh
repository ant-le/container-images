#!/usr/bin/env bash
# No real credentials or host workspaces. Never print pairing tokens or logs.
set -euo pipefail
image=${1:?Usage: verify.sh IMAGE}
prefix="t3code-smoke-$$-$RANDOM"
volume="$prefix-home"
cleanup() {
  docker rm -f "$prefix" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT
docker volume create "$volume" >/dev/null
security=(--platform linux/amd64 --read-only --cap-drop ALL --security-opt no-new-privileges
  --network none --memory 3g --cpus 2 --tmpfs '/tmp:uid=10001,gid=10001,size=1073741824')
# Simulate a fresh local-path mount: root-owned, writable, with no image copy-up.
docker run --rm "${security[@]}" --user 0:0 \
  --mount "type=volume,src=$volume,dst=/home/t3,volume-nocopy" \
  --entrypoint sh "$image" -ec 'chmod 0777 /home/t3; touch /home/t3/.volume-smoke'
docker run --rm "${security[@]}" --tmpfs '/home/t3:uid=10001,gid=10001' --entrypoint sh "$image" -ec '
  test "$(id -u)" = 10001
  mkdir -p "$CODEX_HOME"
  t3 --version
  codex --version
  codex-exec-server --help >/dev/null
  claude --version
  gh --version
  git --version
  node --version
  python3 --version
  mise --version
  gcc --version >/dev/null
  initdb --version
  pg_ctl --version
  test ! -w /usr/local/bin
'
# Exercise compilation on an executable workspace, like the production PVC.
# Docker tmpfs defaults to noexec, unlike that PVC.
docker run --rm "${security[@]}" --tmpfs '/home/t3:uid=10001,gid=10001,exec' \
  --entrypoint bash "$image" -euc '
  cd /home/t3
  printf "int main(void) { return 0; }\n" > probe.c
  gcc probe.c -o probe
  ./probe
  initdb -D /home/t3/pg -U smoke --auth=trust --no-locale > /dev/null
  trap "pg_ctl -D /home/t3/pg -m immediate -w stop >/dev/null" EXIT
  pg_ctl -D /home/t3/pg -l /home/t3/pg.log -o "-h 127.0.0.1 -k /home/t3 -p 55432" -w start >/dev/null
  psql -X -h 127.0.0.1 -p 55432 -U smoke -d postgres -v ON_ERROR_STOP=1 \
    -c "CREATE TABLE probe(id int); INSERT INTO probe VALUES (1);" >/dev/null
  test "$(psql -X -At -h 127.0.0.1 -p 55432 -U smoke -d postgres -c "SELECT count(*) FROM probe")" = 1
'
# Negotiate with the real code-mode helper; codex --version misses incomplete installs.
docker run --rm -i "${security[@]}" --tmpfs '/home/t3:uid=10001,gid=10001' \
  --entrypoint python3 "$image" - <<'PY'
import json, struct, subprocess
hello = json.dumps({'type': 'connection/hello', 'supportedVersions': [1],
                    'requiredCapabilities': [], 'optionalCapabilities': []}).encode()
result = subprocess.run(['codex-code-mode-host', '--listen', 'stdio'],
                        input=struct.pack('<I', len(hello)) + hello,
                        capture_output=True, check=True, timeout=15)
length, = struct.unpack('<I', result.stdout[:4])
reply = json.loads(result.stdout[4:4 + length])
assert reply['type'] == 'connection/ready' and reply['selectedVersion'] == 1, reply
print('PASS: Codex code-mode host handshake')
PY
# Load the real managed policy as the runtime user; unreadable/ignored policy fails.
docker run --rm "${security[@]}" --tmpfs '/home/t3:uid=10001,gid=10001' \
  --entrypoint sh "$image" -ec 'mkdir -p "$CODEX_HOME"; exec codex app-server --print-startup-config' \
  | jq -e '.configRequirements.requirements | .allowedSandboxModes == ["read-only", "workspace-write"] and .allowedApprovalPolicies == ["untrusted", "on-request"]' >/dev/null
start() {
  docker run -d --name "$prefix" "${security[@]}" \
    -v "$volume:/home/t3" "$image" >/dev/null
  for _ in $(seq 1 60); do
    if docker exec "$prefix" curl --fail --silent --max-time 2 \
      http://127.0.0.1:3773/.well-known/t3/environment >/dev/null; then
      return
    fi
    sleep 1
  done
  echo 'T3 startup failed; logs deliberately withheld because they may contain pairing credentials.' >&2
  return 1
}
start
docker exec -i "$prefix" python3 - < "$(dirname "$0")/smoke_skills.py"
docker exec "$prefix" python3 -c 'import json,os; from pathlib import Path; p=Path(os.environ["T3CODE_HOME"])/"userdata/settings.json"; s=json.loads(p.read_text()); assert s["defaultRuntimeMode"] == "approval-required"; assert not s["enableProviderUpdateChecks"]; s["continueThreadsAfterServerUpdate"]=True; p.write_text(json.dumps(s))'
for endpoint in /api/projects /mcp '/ws?orchestrationProtocol=2'; do
  status=$(docker exec "$prefix" curl --silent --show-error --max-time 5 \
    --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:3773$endpoint")
  test "$status" = 401
done
# Generate a pairing code; client redemption is a separate rollout check.
docker exec "$prefix" /opt/t3/t3 pair --ttl 1m >/dev/null 2>&1
docker exec "$prefix" sh -ec 'mkdir -p "$HOME/projects"; echo persistent > "$HOME/projects/smoke.txt"'
descriptor=$(docker exec "$prefix" curl --fail --silent http://127.0.0.1:3773/.well-known/t3/environment)
docker stop --time 30 "$prefix" >/dev/null
docker rm "$prefix" >/dev/null
start
docker exec -i "$prefix" python3 - < "$(dirname "$0")/smoke_skills.py"
docker exec "$prefix" python3 -c 'import json,os; from pathlib import Path; assert json.loads((Path(os.environ["T3CODE_HOME"])/"userdata/settings.json").read_text())["continueThreadsAfterServerUpdate"] is True'
docker exec "$prefix" sh -ec 'test "$(cat "$HOME/projects/smoke.txt")" = persistent'
restored=$(docker exec "$prefix" curl --fail --silent http://127.0.0.1:3773/.well-known/t3/environment)
test "$(jq -er .environmentId <<<"$descriptor")" = "$(jq -er .environmentId <<<"$restored")"
echo 'PASS: non-root tools, conservative fresh defaults, preserved settings, unauthenticated API/WebSocket denial, pairing-code generation and environment/workspace persistence.'
