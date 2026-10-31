FROM docker.io/library/node:24.22.2-trixie-slim@sha256:54f84b4cf8432b21aaffd5d3ce8d41e12cea0d10d33c09d2f3c9879d49d6b446

# Native upstream releases, verified before execution. No runtime installers.
ARG T3_VERSION=0.1.6
ARG T3_SHA256=7cf31da90dee41f524dd5b679ebb6d404637ca5d9dc6fd8bc019511d57bb77e6
ARG CODEX_VERSION=0.166.0
ARG CODEX_SHA256=a0bf572d2f93f056c6712a6da348e96078dc84f3885fd7be67807ef389cb3978
ARG CLAUDE_VERSION=2.1.316
ARG CLAUDE_SHA256=fd13c70000fec3f2116dfbcbb6121210b4a29328bf0e46210a59dd594a67d824
ARG GH_VERSION=2.103.3
ARG GH_SHA256=4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d

RUN test "$(dpkg --print-architecture)" = amd64 \
    && apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends \
       ca-certificates curl git tini python3 ripgrep jq openssh-client unzip \
       bubblewrap socat libatomic1 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 t3 \
    && useradd --uid 10001 --gid t3 --home-dir /home/t3 --create-home t3

RUN mkdir -p /opt/t3 /tmp/codex /tmp/gh \
    && curl -fsSL --retry 3 "https://github.com/pingdotgg/t3code/releases/download/v${T3_VERSION}/t3-${T3_VERSION}-linux-x64.tar.gz" -o /tmp/t3.tgz \
    && echo "${T3_SHA256}  /tmp/t3.tgz" | sha256sum -c - \
    && tar -xzf /tmp/t3.tgz --strip-components=1 -C /opt/t3 \
    && ln -s /opt/t3/t3 /usr/local/bin/t3 \
    && curl -fsSL --retry 3 "https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}/codex-x86_64-unknown-linux-musl.tar.gz" -o /tmp/codex.tgz \
    && echo "${CODEX_SHA256}  /tmp/codex.tgz" | sha256sum -c - \
    && tar -xzf /tmp/codex.tgz -C /tmp/codex \
    && install -m 755 /tmp/codex/codex-x86_64-unknown-linux-musl /usr/local/bin/codex \
    && curl -fsSL --retry 3 "https://downloads.claude.ai/claude-code-releases/${CLAUDE_VERSION}/linux-x64/claude" -o /tmp/claude \
    && echo "${CLAUDE_SHA256}  /tmp/claude" | sha256sum -c - \
    && install -m 755 /tmp/claude /usr/local/bin/claude \
    && curl -fsSL --retry 3 "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_amd64.tar.gz" -o /tmp/gh.tgz \
    && echo "${GH_SHA256}  /tmp/gh.tgz" | sha256sum -c - \
    && tar -xzf /tmp/gh.tgz --strip-components=1 -C /tmp/gh \
    && install -m 755 /tmp/gh/bin/gh /usr/local/bin/gh \
    && rm -rf /tmp/t3.tgz /tmp/codex /tmp/codex.tgz /tmp/claude /tmp/gh /tmp/gh.tgz

# Patch shipped JS dependencies without npm resolution or lifecycle scripts.
# These are patch releases in the same major, with tarball hashes reviewed in Git.
RUN curl -fsSL --retry 3 https://registry.npmjs.org/undici/-/undici-8.12.2.tgz -o /tmp/undici-t3.tgz \
    && echo "a765b5e63ab5c6b1b91a59072c88f7857b36f47b09c376762241d177279cdefd  /tmp/undici-t3.tgz" | sha256sum -c - \
    && curl -fsSL --retry 3 https://registry.npmjs.org/undici/-/undici-6.29.1.tgz -o /tmp/undici-npm.tgz \
    && echo "29c4b8e0b2d86cb8795571c1959012ce057a14ecfd34858335f47c31e770a04f  /tmp/undici-npm.tgz" | sha256sum -c - \
    && curl -fsSL --retry 3 https://registry.npmjs.org/brace-expansion/-/brace-expansion-5.0.11.tgz -o /tmp/brace.tgz \
    && echo "67bb5a1b4d4a8ff497d845a0b891ffe6b7233aea2202641315cec88d0fff15eb  /tmp/brace.tgz" | sha256sum -c - \
    && node -e 'for (const [p,m] of [["/opt/t3/node_modules/undici",8],["/usr/local/lib/node_modules/npm/node_modules/undici",6],["/usr/local/lib/node_modules/npm/node_modules/brace-expansion",5]]) {if (Number(require(p+"/package.json").version.split(".")[0]) !== m) process.exit(1)}' \
    && rm -rf /opt/t3/node_modules/undici /usr/local/lib/node_modules/npm/node_modules/undici /usr/local/lib/node_modules/npm/node_modules/brace-expansion \
    && mkdir -p /opt/t3/node_modules/undici /usr/local/lib/node_modules/npm/node_modules/undici /usr/local/lib/node_modules/npm/node_modules/brace-expansion \
    && tar -xzf /tmp/undici-t3.tgz --strip-components=1 -C /opt/t3/node_modules/undici \
    && tar -xzf /tmp/undici-npm.tgz --strip-components=1 -C /usr/local/lib/node_modules/npm/node_modules/undici \
    && tar -xzf /tmp/brace.tgz --strip-components=1 -C /usr/local/lib/node_modules/npm/node_modules/brace-expansion \
    && rm /tmp/undici-t3.tgz /tmp/undici-npm.tgz /tmp/brace.tgz

COPY --chmod=755 collect-sources.py /tmp/collect-sources.py
RUN python3 /tmp/collect-sources.py \
    && rm /tmp/collect-sources.py \
    && rm -rf /var/lib/apt/lists/*
COPY licenses/ /usr/share/doc/t3code-bundle/
COPY --chmod=755 entrypoint.sh /opt/t3/entrypoint.sh
COPY --chmod=644 codex-requirements.toml /etc/codex/requirements.toml
COPY --chmod=644 claude-managed-settings.json /etc/claude-code/managed-settings.json
COPY --chmod=644 default-settings.json /opt/t3/default-settings.json
RUN chmod 0755 /etc/codex /etc/claude-code \
    && mkdir -p /home/t3/.codex /home/t3/.claude /home/t3/.t3 /home/t3/.config/gh /home/t3/projects \
    && chown -R 10001:10001 /home/t3

ENV HOME=/home/t3 \
    T3CODE_HOME=/home/t3/.t3 \
    CODEX_HOME=/home/t3/.codex \
    CLAUDE_CONFIG_DIR=/home/t3/.claude \
    GH_CONFIG_DIR=/home/t3/.config/gh \
    DISABLE_AUTOUPDATER=1 \
    T3CODE_TELEMETRY_ENABLED=false \
    PATH=/usr/local/bin:/usr/bin:/bin
USER 10001:10001
WORKDIR /home/t3
EXPOSE 3773
ENTRYPOINT ["/opt/t3/entrypoint.sh"]
CMD ["serve", "--host", "0.0.0.0", "--port", "3773", "--no-browser"]
