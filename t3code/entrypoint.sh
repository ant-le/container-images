#!/bin/sh
set -eu
umask 077
# Kubernetes mounts an empty PVC, unlike Docker's initial volume copy-up.
mkdir -p "$T3CODE_HOME" "$CODEX_HOME" "$CLAUDE_CONFIG_DIR" "$GH_CONFIG_DIR" "$HOME/projects"
# Seed a fresh environment only. Preserve operator settings across replacement.
python3 - <<'PY'
import os
from pathlib import Path
path = Path(os.environ['T3CODE_HOME']) / 'userdata/settings.json'
path.parent.mkdir(parents=True, exist_ok=True)
try:
    with path.open('x') as target:
        target.write(Path('/opt/t3/default-settings.json').read_text())
except FileExistsError:
    pass
PY
exec /usr/bin/tini -s -- /opt/t3/t3 "$@"
