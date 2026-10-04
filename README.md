# Customized upstream container images

Build recipes and smoke tests for upstream software we customize. This private
repository publishes software-only images to GHCR; it has no cluster access.
Our own applications keep their source, Dockerfiles and tests in their own repos.

| Image | Recipe | Verification |
| --- | --- | --- |
| `ghcr.io/ant-le/openclaw` | `openclaw/Dockerfile` | `openclaw/verify.sh` |

OpenClaw bundles the pinned upstream gateway, whisper.cpp, FFmpeg, a multilingual
Whisper model, the version-matched free search and OpenShell sandbox plugins,
and checksum-pinned OpenShell CLI 0.1.2. Runtime agents,
credentials, model policy, networking and storage live in
[`ant-le/server`](https://github.com/ant-le/server).

## Local checks

```sh
mise trust
mise install
mise run check
mise run openclaw:build
mise run openclaw:verify
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
