# Container images

This repository owns build recipes, patches, and smoke tests for customized
upstream software. Application source and Dockerfiles for our own apps belong
in their application repositories. Deployment configuration belongs in
`ant-le/server`; never put cluster credentials or runtime data here.

- Read README.md before changing the release workflow.
- Run `mise run check`. For T3 changes, also build the image and run
  `bash t3code/verify.sh <local-image>` when Docker is available.
- GitHub Actions builds once, smoke-tests that image, then pushes that same
  local image to GHCR. Do not manually publish images.
- Preserve digest/checksum pins; do not include credentials or private files
  in image build contexts.
- Publishing is not deployment. The server repository selects an immutable
  digest through a separate deployment PR. Do not merge that PR, change live
  workloads, or alter production without explicit approval.
- Do not commit, push, create releases, or trigger workflows without approval.
- Report local validation, image publication, GitOps rollout, and end-to-end
  application testing as separate stages.
