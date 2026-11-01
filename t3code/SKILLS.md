# Personal skills in T3

Status: local implementation; publication and authenticated T3 acceptance are
separate release gates. This adds an instruction-only skill writer, not a plugin
installer, credential broker, or general-purpose configuration editor.

## Everyday use

Ask either agent: “Create a personal skill called review-app for reviewing my
application changes.” The `t3-skills` MCP server exposes three tools:

- `save_skill(name, description, instructions, expected_revision?)` creates a
  personal skill in both providers. No raw YAML/frontmatter or paths are accepted.
- `get_skill(name)` returns a previously saved skill and its revision. Supply that
  revision to save an update; stale updates are refused. Identical retries repair
  an interrupted installation.
- `list_skills()` lists names managed by this service.

Restart the agent session in T3 after saving. Select `$t3-user-review-app` in the
composer. These are personal skills shared across projects in this environment;
updates affect future sessions. The tool reports installation, not proof that
T3 or the selected provider has refreshed its skill catalog. If a provider's Read
rule blocks its global directory, use `get_skill` to inspect the managed text;
do not weaken that rule or copy credentials.

Skills are limited to a name, description, and up to 24,000 UTF-8 bytes of
instructions. There can be at most 100 managed skills. Supporting scripts,
assets, plugin files, hooks and dynamic command expansion are unsupported.
Deletion is currently an operator action. Never submit credentials in skill text.
Instructions can describe ordinary shell work, but execution remains governed by
the agent's existing permissions. Skill prose is not a security boundary.

## Implementation and trust

`skills_mcp.py` is an image-owned Python stdio MCP server. It opens no network
listener, starts no subprocess, accepts no caller-selected filesystem path, and
executes no submitted content. Python runs with `-I` and the server clears its
inherited environment. Fixed storage paths are:

- `/home/t3/.t3-skills/<name>.json`: authoritative bounded record and revision source.
- `/home/t3/.agents/skills/t3-user-<name>/SKILL.md`: Codex installation.
- `/home/t3/.claude/skills/t3-user-<name>/SKILL.md`: Claude installation.

The image keeps the canonical store protected from direct agent access. Claude
also cannot edit the Codex skill tree; Codex's existing policy denies the Claude
home. Existing credential/state denials and sandbox requirements remain in force.
The service runs outside the command sandbox as UID 10001, like other local MCP
servers: its small, reviewed implementation is part of the trusted boundary. It
has no OS-level identity separating it from provider credentials. This is a
restricted API, not a claim that an arbitrary compromised MCP process cannot
access this user's files. No generic file-read, file-write, shell or plugin tool
is exposed.

All path components are opened without following symlinks. Reads require bounded
regular, singly linked records with the exact skill schema. Writes replace files
atomically rather than opening existing destinations; linked files, foreign
skills and directories containing extra files are refused. Generated frontmatter
has only `name` and `description`; instruction text cannot introduce additional
frontmatter keys. Dynamic `!`-backtick expansion is rejected. A process lock and
revision checks prevent silent concurrent updates. If a mirror write fails, the
response reports partial installation and the same save can repair it.

The service does not inspect credentials to detect pasted secrets. It stores the
text the caller supplies and returns it to authorized agents through `get_skill`.
Do not treat this as a secret scanner or use it for confidential account material.

## Registration and lifecycle

Codex loads the additive MCP entry from image-owned `/etc/codex/config.toml`.
At T3 startup, the entrypoint registers the same stdio command in the custom
Claude config home's `.claude.json`. Registration preserves unrelated settings,
is idempotent, and fails closed on an existing conflicting `t3-skills` entry or
unsafe config path. No `managed-mcp.json` is installed: its exclusive-server
semantics would conflict with T3's own MCP connection. The startup registration
reads/writes provider configuration outside the agent sandbox; it does not return
that configuration through the MCP tools.

The root-owned policies and tool ship through the ordinary image release.
Skills then persist on the existing home PVC and need no rebuild for content
updates. No live settings are changed by building this image locally. Rollback
removes tool availability but does not erase personal skill files or the Claude
registration on the PVC; operator cleanup should preserve wanted skills and
remove only this registration if the old image lacks the tool.

## Verification

`mise run check` includes security and persistence tests with dummy files.
`bash t3code/verify.sh IMAGE` exercises MCP transport, native provider registration,
and skill persistence across fresh processes and container replacement, with no
network or real credentials. Native Codex `skills/list` and Claude's SDK
initialization also verify that both providers discover the installed skill.
These checks do not prove authenticated T3 skill invocation or sandbox behavior
on Hetzner.

Local verification on 2026-11-01 passed: 13 security/behavior tests, the image
build, the complete image smoke test, and the scan gate (zero fixable
High/Critical findings; 463 total matches still require release review). The
server repository's checks also passed: 32 tests and 460 policy checks, with five
skips. A read-only live metadata check confirmed the expected Claude config file
is a regular file; no live configuration or credential contents were read.

After approved publication and manual rollout, test fresh Codex and Claude T3
sessions in Auto-accept edits mode:

1. Discover the three tools and create one harmless uniquely named skill.
2. Restart the agent session and invoke it from the composer in each provider.
3. Update it with the current revision; reject a stale revision and an attempted
   raw hook/command/path argument. Verify the changed behavior in a fresh session.
4. Use dummy credential fixtures to verify protected storage remains inaccessible
   through ordinary commands/file tools. Never read real login files for testing.
5. Confirm the regular T3 tools still work; record installed version and results.

Sources: [MCP stdio](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports),
[Claude protected paths](https://code.claude.com/docs/en/sandboxing#protected-paths),
[Claude user MCP scope](https://code.claude.com/docs/en/mcp#user-scope),
[T3 reload](https://github.com/pingdotgg/t3code/blob/v0.1.6/docs/user/composer.md#commands-and-skills).
