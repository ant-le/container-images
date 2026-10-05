FROM ghcr.io/astral-sh/uv:0.12.15@sha256:62f8c047d0a0e9ece6b53fc63df902585a67a47a7f318ddec4a37db586edc8e3 AS uv
FROM docker.io/library/python:3.12.12-slim-bookworm@sha256:593bd06efe90efa80dc4eee3948be7c0fde4134606dd40d8dd8dbcade98e669c

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl jq sqlite3 unzip util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && /usr/local/bin/python3 -m pip uninstall --yes pip \
    && groupadd --gid 10001 agent \
    && useradd --uid 10001 --gid agent --home-dir /sandbox --create-home agent
COPY --from=uv /uv /usr/local/bin/uv
COPY python-env.sh /opt/agent/bin/python-env
COPY profile.sh /etc/profile.d/agent-shell.sh
RUN chmod 755 /opt/agent/bin/python-env \
    && for name in python python3 uv; do ln -s python-env /opt/agent/bin/$name; done

# Libraries persist on each sandbox's PVC, outside OpenClaw's mirrored roots.
# No extra interpreter downloads or source compilation by default.
ENV HOME=/sandbox \
    VIRTUAL_ENV=/sandbox/.venv \
    PATH=/opt/agent/bin:/sandbox/.venv/bin:/usr/local/bin:/usr/bin:/bin \
    UV_CACHE_DIR=/sandbox/.cache/uv \
    UV_PYTHON_DOWNLOADS=never \
    UV_NO_BUILD=1
USER 10001:10001
WORKDIR /sandbox
CMD ["sleep", "infinity"]
