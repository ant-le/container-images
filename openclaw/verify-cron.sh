#!/bin/sh
# Run inside a disposable image container: no real credentials, model or chats.
set -eu
umask 077
export OPENCLAW_CONFIG_PATH=/tmp/cron-smoke.json
cat > "$OPENCLAW_CONFIG_PATH" <<'JSON'
{"gateway":{"mode":"local","bind":"loopback","auth":{"mode":"token","token":"disposable-cron-smoke"}},"agents":{"defaults":{"model":{"primary":"probe/probe"},"heartbeat":{"every":"0m"}}},"models":{"providers":{"probe":{"baseUrl":"http://127.0.0.1:18888/v1","apiKey":"synthetic","api":"openai-completions","models":[{"id":"probe","name":"probe","contextWindow":32768,"maxTokens":1024}]}}},"plugins":{"allow":[]},"cron":{"enabled":true,"triggers":{"enabled":true}}}
JSON

# Deterministic local model: prove timed agent dispatch without external calls.
node - <<'JS' &
const http = require("http");
http.createServer((req, res) => {
  let body = "";
  req.on("data", data => body += data);
  req.on("end", () => {
    if (req.method === "GET") {
      res.writeHead(200, {"Content-Type": "application/json"});
      res.end(JSON.stringify({object: "list", data: [{id: "probe", object: "model"}]}));
      return;
    }
    const base = {id: "cron-smoke", model: "probe", created: 1};
    if (JSON.parse(body || "{}").stream) {
      res.writeHead(200, {"Content-Type": "text/event-stream"});
      for (const [delta, finish_reason] of [[{role: "assistant", content: "CRON_AGENT_OK"}, null], [{}, "stop"]]) {
        res.write("data: " + JSON.stringify({...base, object: "chat.completion.chunk", choices: [{index: 0, delta, finish_reason}]}) + "\n\n");
      }
      res.end("data: [DONE]\n\n");
    } else {
      res.writeHead(200, {"Content-Type": "application/json"});
      res.end(JSON.stringify({...base, object: "chat.completion", choices: [{index: 0, message: {role: "assistant", content: "CRON_AGENT_OK"}, finish_reason: "stop"}]}));
    }
  });
}).listen(18888, "127.0.0.1");
JS
model_pid=$!

gateway_pid=
cleanup() {
  if [ -n "$gateway_pid" ]; then
    kill "$gateway_pid" 2>/dev/null || true
    wait "$gateway_pid" 2>/dev/null || true
  fi
}
trap 'cleanup; kill "$model_pid" 2>/dev/null || true' EXIT INT TERM
start_gateway() {
  openclaw gateway > /tmp/cron-smoke.log 2>&1 &
  gateway_pid=$!
  if ! curl --fail --silent --retry 60 --retry-delay 1 --retry-connrefused \
    --retry-max-time 90 http://127.0.0.1:18789/startupz > /dev/null; then
    cat /tmp/cron-smoke.log >&2
    exit 1
  fi
}
start_gateway

# Exercise the strongest direct admin API, including records restored at startup.
openclaw automations add --name forbidden-command --at 1d --disabled \
  --command-argv '["sh","-c","touch /tmp/cron-command-escaped"]' > /tmp/command-job.json
job_id=$(node -e 'console.log(require("/tmp/command-job.json").id)')
test -n "$job_id"
openclaw automations add --name forbidden-on-exit \
  --on-exit 'touch /tmp/cron-exit-escaped; sleep 30' \
  --session isolated --message synthetic --no-deliver > /dev/null
openclaw automations add --name forbidden-stream \
  --stream-command '["sh","-c","touch /tmp/cron-stream-escaped; sleep 30"]' \
  --session isolated --message synthetic --no-deliver > /dev/null
openclaw automations add --name ordinary-agent-job --at 1d \
  --session isolated --message synthetic --no-deliver --tools '' > /tmp/agent-job.json
node -e 'const j=require("/tmp/agent-job.json"); if(j.payload.kind!=="agentTurn" || j.schedule.kind!=="at") process.exit(1)'

cleanup
gateway_pid=
start_gateway
# A rejected run may also give the CLI a nonzero exit; inspect persisted evidence.
openclaw automations run "$job_id" --wait --wait-timeout 60s > /tmp/command-run.json 2>&1 || true
openclaw automations runs "$job_id" --limit 1 > /tmp/command-history.json
node -e '
  const d=require("/tmp/command-history.json");
  if (!d.entries.some(e => e.status === "error" && String(e.error).includes("Gateway command jobs are disabled"))) {
    throw new Error("Gateway command execution was not rejected");
  }
'
test ! -e /tmp/cron-command-escaped
test ! -e /tmp/cron-exit-escaped
test ! -e /tmp/cron-stream-escaped
agent_job_id=$(node -e 'console.log(require("/tmp/agent-job.json").id)')
openclaw automations run "$agent_job_id" --wait --wait-timeout 90s > /tmp/agent-run.json
openclaw automations runs "$agent_job_id" --limit 1 > /tmp/agent-history.json
node -e '
  const d=require("/tmp/agent-history.json");
  if (!d.entries.some(e => e.status === "ok" && String(e.summary).includes("CRON_AGENT_OK"))) {
    throw new Error("Timed agent execution did not complete through the local model");
  }
'
echo 'PASS: gateway cron processes blocked, including persisted jobs; timed agent execution completed'
