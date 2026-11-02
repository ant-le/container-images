Third-party software notices

mise 2026.10.25: MIT, see mise-MIT.txt.

T3 Code 0.1.6: MIT, T3 Tools Inc. See T3-MIT.txt.
Codex CLI 0.166.0: Apache-2.0, OpenAI. See Codex-Apache-2.0.txt
and Codex-NOTICE.txt.
GitHub CLI 2.103.3: MIT, GitHub Inc. See GitHub-CLI-MIT.txt.
Node.js 24.22.2 and T3's embedded Node.js 26.12.1: see the matching
Node-*-LICENSE.txt bundles, including their third-party notices.
Codex's Ratatui dependency: see Ratatui-MIT.txt and Codex-NOTICE.txt.
Claude Code 2.1.316: Copyright Anthropic PBC. All rights reserved.
The unmodified binary remains subject to Anthropic's applicable terms:
https://code.claude.com/docs/en/legal-and-compliance
Each user must authenticate using their own permitted credentials.
This bundle is not endorsed by these upstream publishers.

Debian package copyrights and licenses are retained under /usr/share/doc.
Node/npm and bundled JavaScript dependencies retain their own license files.
The image recipe replaces bundled Undici with upstream 8.12.2 (T3) and
6.29.1 (npm), and npm brace-expansion with upstream 5.0.11, without modifying
those replacement tarballs. See their included licenses.

Exact Debian corresponding-source archives, including Debian build/patch
metadata, are supplied inside this image at /usr/share/source/t3code-bundle.
packages.tsv maps installed binaries to source versions; SHA256SUMS identifies
all source files. Archives are obtained through authenticated Debian indexes
at build time. No separate access request or private GitHub account is needed.
Extract without executing the image:
  docker create --name t3-sources IMAGE
  docker cp t3-sources:/usr/share/source/t3code-bundle ./sources
  docker rm t3-sources
