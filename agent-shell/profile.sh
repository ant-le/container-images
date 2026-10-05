#!/bin/sh
# Debian login shells reset PATH. Keep the same tools for login and exec shells.
export PATH=/opt/agent/bin:/sandbox/.venv/bin:/usr/local/bin:/usr/bin:/bin
# OpenShell SSH exec does not inherit arbitrary OCI environment variables.
export VIRTUAL_ENV="${VIRTUAL_ENV:-/sandbox/.venv}"
export UV_CACHE_DIR="${UV_CACHE_DIR:-/sandbox/.cache/uv}"
export UV_PYTHON_DOWNLOADS="${UV_PYTHON_DOWNLOADS:-never}"
export UV_NO_BUILD="${UV_NO_BUILD:-1}"
