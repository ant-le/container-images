// OpenClaw 2026.9.7: command payloads and watcher sources bypass sandbox exec.
// Disable those execution paths for all callers, including owner/admin turns
// and previously stored jobs. Time-based agent jobs are unaffected.
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";

const file = "/app/dist/server-cron-CTlLFUyb.mjs";
let source = readFileSync(file, "utf8");
const expected = "0ab0ecf5b7b49e7fa3ffd7f933e32d478f2cb5160ceb252989582f66d9a96702";
if (createHash("sha256").update(source).digest("hex") !== expected) {
  throw new Error("Cron execution code changed; review the gateway-shell restriction before upgrading");
}
for (const [before, after] of [
  [
    "async function runCronCommandJob(params) {",
    'async function runCronCommandJob(params) {\n\treturn { status: "error", error: "Gateway command jobs are disabled; use a sandboxed agentTurn job" };',
  ],
  [
    'function isWatchableExitJob(job) {\n\treturn job.enabled && job.schedule.kind === "on-exit";\n}',
    'function isWatchableExitJob(job) {\n\treturn false; // Gateway process watchers are disabled.\n}',
  ],
  [
    'function isCronStreamJob(job) {\n\treturn job.schedule.kind === "stream";\n}',
    'function isCronStreamJob(job) {\n\treturn false; // Gateway process watchers are disabled.\n}',
  ],
]) {
  if (source.split(before).length !== 2) throw new Error("Cron patch anchor is not unique");
  source = source.replace(before, after);
}
writeFileSync(file, source);
