# Customized upstream container images

Build recipes and smoke tests for upstream software we customize. This private
repository publishes software-only images to GHCR; it has no cluster access.
Our own applications keep their source, Dockerfiles and tests in their own repos.

| Image | Recipe | Verification |
| --- | --- | --- |
| `ghcr.io/ant-le/openclaw` | `openclaw/Dockerfile` | `openclaw/verify.sh` |
| `ghcr.io/ant-le/agent-shell` | `agent-shell/Dockerfile` | `agent-shell/verify.sh` |

OpenClaw bundles the pinned upstream gateway, whisper.cpp, FFmpeg, a multilingual
Whisper model, the version-matched free search and OpenShell sandbox plugins,
and checksum-pinned OpenShell CLI 0.1.2. Runtime agents,
credentials, model policy, networking and storage live in
[`ant-le/server`](https://github.com/ant-le/server).

The image also packages a restricted company profile of ownCloud MCP v1.1.0.
`openclaw/ocis-mcp/` reuses upstream discovery/SDK code and adds bounded text
writing and binary transfer over authenticated OpenShell SSH. Transfers stage
outside mirrored workspaces; no OpenClaw/storage-backend patch is required.
Its tools require per-agent runtime credentials and an explicit Space allowlist
from `server`'s nonsecret scope registry. Catalog and transfer profiles use the
same identity/scope; listing, search and direct reads cannot use another agent's
Spaces through the connector. Search requires one allowed Space ID. Transfers
also require a fixed sandbox binding; catalog-only agents need no sandbox.
Four scoped catalog/text connectors are active in the server deployment;
binary-transfer connectors remain disabled. This checkout's exact-path write
contract is pending image publication and GitOps rollout.
The image build runs Go and Python security/transfer tests before packaging.

Catalog tool `ocis_write_text(space_id, path, content)` creates or replaces UTF-8
files (up to 100 KiB). Transfer tool
`ocis_publish_output(space_id, path, transfer_id, sha256)` publishes a staged
binary (up to 32 MiB) with the same exact-path create/replace behavior. Both accept
root filenames or nested relative paths in **only the agent's configured output
Space**, create missing parent folders, and return the requested `path` plus
verified `size` and `sha256`. Existing parent paths must be DAV collections.
Invoking either tool with an existing file path explicitly replaces that file:
writes are last-writer-wins, without atomic compare-and-swap or concurrent-update
protection. Read-back checksum verification confirms the bytes observed at that
time, not a lasting lock. oCIS 8.2.1 does not enforce `If-None-Match` on PUT.
`ocis_write_text` replaces the former `ocis_create_text` name; no alias is exposed.
There is no remote `outputs/<random>/` allocation or mandatory `outputs/` prefix.
Random local transfer tickets and sandbox staging confinement are unchanged.
The connector does not expose remote delete, move or copy, and cannot write to
canonical or other agents' Spaces. Existing backend Editor access to the own
output Space is sufficient; no UI permission change is needed.

The lightweight agent-shell image includes Python, uv, curl, jq, SQLite and
unzip. It runs as UID 10001. Python/uv initialize a per-agent virtual
environment at `/sandbox/.venv` on first use, serialized with flock. The image's
standard library is available there; optional data/PDF/spreadsheet
libraries install with `uv pip install` (uv's own installer; pip is removed).
Wheels are preferred exclusively by default, and
uv never downloads another Python interpreter. `/sandbox` must be a persistent,
writable, private volume. Keep `.venv` and `.cache` outside mirrored workspaces;
retain dependency requirements with the project. An interpreter upgrade requires
rebuilding the environment from those requirements. `uv cache clean`
reclaims download caches without uninstalling packages.

OpenClaw's small `openshell-agent` launcher supplies the deployment's
`OPENCLAW_SANDBOX_MEMORY` to sandbox creation. It leaves supervisors and all other
CLI operations unchanged. Network policy and memory budgets belong in server.

The pinned gateway disables cron command payload execution and on-exit/stream
process watchers for **all** callers, including owner/admin model turns and
previously stored jobs. Upstream 2026.9.7 executes these on the gateway outside
agent sandbox policy. Ordinary time-based agent jobs remain supported. The small
build-time patch verifies the complete upstream module checksum and fails closed
on version drift; review it on every upgrade. Deployment also disables advanced
cron triggers so unsupported stream/script requests are rejected early.

## Local checks

```sh
mise trust
mise install
mise run check
mise run openclaw:build
mise run openclaw:verify
mise run agent-shell:build
mise run agent-shell:verify
```

The smoke test downloads a checksum-pinned public audio sample and tests OGG/Opus
transcription. It does not prove Telegram-to-OpenClaw voice delivery.

## Release path

1. Open a PR here. CI validates workflows and smoke-tests the image.
2. After merge to `main`, CI builds once, tests it, and pushes that **same image**
   to GHCR. SHA tags and OCI source/revision labels provide traceability.
3. The Actions summary reports the immutable digest and the command to propose
   its deployment from the server checkout:
   `mise run openclaw:release -- sha256:<digest> <full-source-commit>`.
4. The command checks anonymous image access and source labels, then opens a
   deployment PR. Review its checks before merging. ArgoCD follows server `main`.
5. Verify the rollout and real inbound Telegram audio after deployment.

Keep the first `ghcr.io/ant-le/openclaw` package public for anonymous node pulls.
The `ghcr.io/ant-le/agent-shell` package must also be public; select its immutable
digest in OpenClaw's OpenShell `from` setting in the server deployment PR.
GitHub creates new packages private by default: change package visibility in
GitHub package settings after first publication. Never embed private config or
data in the build context. Repository visibility remains private.

The previous `ghcr.io/ant-le/server-openclaw` package remains available for the
currently deployed digest and rollback. Do not delete it during migration.

## Shared publishing workflow

`.github/workflows/container.yml` provides the common build/test/publish job.
Add another customized upstream image with a directory, a smoke script taking
one image argument, and a caller workflow with path filters. Publishing uses
only the publishing repository's `GITHUB_TOKEN`.

The initial deployment handoff uses the operator's existing local `gh` login;
CI has no credential granting access to the server repository. To automate PR
creation later, use a GitHub App installed only on `ant-le/server`, with Contents
and Pull requests write permissions. The server release helper also works with
the App installation token in `GH_TOKEN`; never copy a personal CLI token into CI.
