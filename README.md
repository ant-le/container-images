# Customized upstream container images

Software-only build recipes for `ant-le/server`. No credentials, workspaces or
cluster configuration belong in this repository.

The active image is `ghcr.io/ant-le/t3code`: T3 Code 0.1.6, Codex 0.166.0,
Claude Code 2.1.316, GitHub CLI 2.103.3 and Node 24.22.2 on Debian trixie.
The Dockerfile pins the base digest and verifies native release checksums.
It also checksum-pins same-major security fixes for T3's Undici (8.12.2),
npm's Undici (6.29.1) and brace-expansion (5.0.11). These replace bundled
packages without invoking a resolver or lifecycle scripts; recheck them on
upstream upgrades. Checksums establish content identity, not upstream safety.
Keep the full T3 distribution: the executable needs its sibling files.
Codex also needs its matching `codex-code-mode-host` and `codex-exec-server`
release binaries beside `codex`; all three artifacts are checksum-pinned.
Tools live outside the writable home; runtime credentials belong on the server PVC.

Run `mise run check`, `mise run t3code:build`, `mise run t3code:verify`, then
`mise run t3code:scan`. The latter writes `/tmp/t3code-scan.json`.
The smoke test uses disposable Docker storage and no real provider credentials.
It checks non-root tools, the Codex code-mode helper handshake, read-only filesystem
startup without network, pairing-code generation,
and environment/workspace persistence across container replacement. Real provider
turns, browser/WebSocket access through Tailscale and sandbox compatibility still
need end-to-end acceptance after deployment.

Fresh homes start in Approval required mode, with provider updates, automatic
thread continuation and browser downloads disabled. The image's root-owned
Codex requirements reject Full access and constrain approvals; Claude managed
settings disable bypass and require its OS sandbox. Both deny agent access to
provider, GitHub, SSH and T3 credential/state paths. These policies are immutable
on the deployed read-only root; user settings cannot relax them. Real T3/provider
turns still require acceptance on the target host. Failure must stop activation.

## Publication

`.github/workflows/t3code.yml` contains separate check, build and publish jobs.
A PR validates, builds, smoke-tests and scans without package-write permission.
A push to main runs the same checks, then transfers that tested image to a fresh
publisher runner using an immutable artifact ID and SHA256 archive checksum.
Only the publisher receives package-write permission; it neither builds nor runs
the image. It verifies the config digest before and after pulling the published
digest, then uploads a publication receipt bound to the source SHA and run ID.
Fresh builds bypass apt layer caching. Grype 0.121.0 is checksum-pinned and
blocks fixable High/Critical findings or an invalid/database older than five days.
All other matches remain in `vulnerability-report` for vendor/reachability review.
Passing this gate is not a CVE-free claim or approval of unfixed findings.

No manual image publication or workflow-dispatch path. Publication does not deploy. Before selecting a digest, review its complete CI scan report
and record remaining finding dispositions; a local scan does not cover the CI build.

The GHCR package is intended for public, anonymous pulls; the source repository
can remain private. Preserve the bundled notices and exact Debian corresponding
source archives under `/usr/share/source/t3code-bundle`. Source collection fails
closed if a package's exact source version is unavailable. This adds compressed
archives to the image, without a separate publication workflow. The unmodified
Claude binary remains subject to Anthropic's terms and each user's own login.
GitHub initially creates packages private; visibility is changed only after the
redistribution-ready image is published. Remove any older private-only versions
before making the package public, because visibility applies to all versions.
After publication, record the digest and source SHA. The server release helper
verifies registry content, platform, source labels, main ancestry, and the matching
successful push-run publication receipt before proposing a deployment PR.
The receipt is retained for 90 days as an audit record; proposals require a scan
database no older than five days. Older rollbacks use a reviewed Git revert of
the deployment digest and manual Argo sync, accounting for state migrations.
This is GitHub run/artifact verification, not an independent signed attestation.
Enforced branch review remains a prerequisite before unattended promotion.
The first deployment additionally enables its staged Application after
selecting the real digest. Manual ArgoCD sync is a separate approved action.

OpenClaw (including Whisper) and agent-shell recipes and caller workflows are
retired. Their history and already published packages remain available for
recovery; this change does not delete registry images or running workloads.
The server retirement runbook owns encrypted backup and live cleanup.
