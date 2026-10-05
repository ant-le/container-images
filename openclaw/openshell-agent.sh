#!/usr/bin/env bash
# The pinned plugin supports a CLI command override but no memory option.
# Keep runtime sizing in deployment configuration, not a patched plugin bundle.
set -euo pipefail
# Operators may pause plugin access during a retained-volume migration. Agents
# cannot reach this gateway-only path; administrative CLI calls bypass the wrapper.
if [[ -f "$HOME/.config/openclaw/openshell-maintenance" ]]; then
  echo 'Sandbox maintenance in progress; retry after the operator finishes.' >&2
  exit 75
fi
args=("$@")
for ((i=0; i+1<${#args[@]}; i++)); do
  [[ ${args[i]} != -- ]] || break
  if [[ ${args[i]} == sandbox && ${args[i+1]} == create ]]; then
    args=("${args[@]:0:i+2}" --memory "${OPENCLAW_SANDBOX_MEMORY:?Set the sandbox memory budget}" "${args[@]:i+2}")
    break
  fi
done
exec /usr/local/bin/openshell "${args[@]}"
