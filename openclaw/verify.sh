#!/usr/bin/env bash
# Smoke-test the runtime image; does not access the cluster or Telegram.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <local-image>" >&2
  exit 2
fi

docker run --rm --platform linux/amd64 --entrypoint sh "$1" -ec '
  set -eu
  command -v ffmpeg
  command -v whisper-cli
  command -v ocis-company-mcp
  openshell --version | grep -F "0.1.2"
  openshell-agent --version | grep -F "0.1.2"
  OPENCLAW_SANDBOX_MEMORY=512Mi openshell-agent --gateway test sandbox create --help >/dev/null
  mkdir -p /tmp/maintenance-home/.config/openclaw
  touch /tmp/maintenance-home/.config/openclaw/openshell-maintenance
  code=0
  HOME=/tmp/maintenance-home openshell-agent --version >/dev/null 2>&1 || code=$?
  test "$code" = 75
  node -e '\''
    require("fs").writeFileSync("/tmp/plugin-config.json", JSON.stringify({plugins: {
      allow: ["openshell"],
      load: {paths: ["/opt/openclaw-plugins/openshell/node_modules/@openclaw/openshell-sandbox"]},
      entries: {openshell: {enabled: true}}
    }}));
  '\''
  OPENCLAW_CONFIG_PATH=/tmp/plugin-config.json openclaw plugins list --json > /tmp/plugins.json
  node -e '\''
    const data = require("/tmp/plugins.json");
    if (!data.plugins.some(p => p.id === "openshell" && p.status === "loaded")) {
      throw new Error("OpenShell plugin did not load through OpenClaw");
    }
  '\''
  test -r /opt/whisper/ggml-small-q5_1.bin
  curl --fail --location --silent --show-error \
    https://raw.githubusercontent.com/ggml-org/whisper.cpp/927cfce34f31707e17f2bff35c349632fb9e2c3a/samples/jfk.wav \
    -o /tmp/jfk.wav
  echo "59dfb9a4acb36fe2a2affc14bacbee2920ff435cb13cc314a08c13f66ba7860e  /tmp/jfk.wav" | sha256sum -c -
  ffmpeg -hide_banner -loglevel error -i /tmp/jfk.wav -c:a libopus /tmp/jfk.ogg
  if ! whisper-cli -m /opt/whisper/ggml-small-q5_1.bin -l en -t 2 -nt -f /tmp/jfk.ogg > /tmp/transcript.txt 2> /tmp/whisper.log; then
    cat /tmp/whisper.log >&2
    exit 1
  fi
  if ! grep -Fi "ask not what your country can do for you" /tmp/transcript.txt; then
    cat /tmp/transcript.txt >&2
    exit 1
  fi
'

docker run --rm -i --platform linux/amd64 --entrypoint sh "$1" -s \
  < "$(dirname "$0")/verify-cron.sh"
