#!/bin/sh
# OpenShell does not run the image ENTRYPOINT. Initialize on first Python/tool use.
set -eu
# shellcheck disable=SC1091
. /etc/profile.d/agent-shell.sh
if [ ! -f "$VIRTUAL_ENV/.agent-initialized" ] || [ ! -x "$VIRTUAL_ENV/bin/python3" ]; then
  (
    flock 9
    if [ ! -f "$VIRTUAL_ENV/.agent-initialized" ] || [ ! -x "$VIRTUAL_ENV/bin/python3" ]; then
      # Allow repair after an interrupted bootstrap without removing packages.
      /usr/local/bin/uv venv --allow-existing --python /usr/local/bin/python3.12 "$VIRTUAL_ENV"
      touch "$VIRTUAL_ENV/.agent-initialized"
    fi
  ) 9>"$VIRTUAL_ENV.lock"
fi
case "${0##*/}" in
  python|python3) exec "$VIRTUAL_ENV/bin/python3" "$@" ;;
  uv) exec /usr/local/bin/uv "$@" ;;
  *) echo 'Invoke this launcher through python, python3 or uv.' >&2; exit 2 ;;
esac
