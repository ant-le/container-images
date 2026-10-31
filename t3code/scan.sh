#!/usr/bin/env bash
# Scan only the specified software image, never the workstation filesystem.
set -euo pipefail
image=${1:?Usage: scan.sh IMAGE REPORT.json}
report=${2:?Usage: scan.sh IMAGE REPORT.json}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) archive=grype_0.121.0_linux_amd64.tar.gz; expected=64e07133d3486115de647a66581461dccf5c732748a0a3119077395565f15519 ;;
  Darwin/arm64) archive=grype_0.121.0_darwin_arm64.tar.gz; expected=7be39d3a0428c664ce0183e4aba31ed9accffce9eda7e7f6fa84e9c512285f27 ;;
  *) echo 'Unsupported scanner host architecture' >&2; exit 1 ;;
esac
curl --proto '=https' --tlsv1.2 -fsSL --retry 3 "https://github.com/anchore/grype/releases/download/v0.121.0/$archive" -o "$work/grype.tgz"
printf '%s  %s\n' "$expected" "$work/grype.tgz" | shasum -a 256 -c -
tar -xzf "$work/grype.tgz" -C "$work" grype
# Grype does not select Docker CLI contexts automatically (notably OrbStack).
if [[ "$image" == docker:* && -z ${DOCKER_HOST:-} ]]; then
  DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
  export DOCKER_HOST
fi
"$work/grype" "$image" -o json --file "$report"
python3 - "$report" <<'PY'
import json, sys
from datetime import datetime, timezone, timedelta
with open(sys.argv[1]) as f:
    report = json.load(f)
db = report['descriptor']['db']['status']
built = datetime.fromisoformat(db['built'].replace('Z', '+00:00'))
if not db['valid'] or not timedelta(0) <= datetime.now(timezone.utc) - built <= timedelta(days=5):
    raise SystemExit('Vulnerability database is invalid, stale or future-dated')
blocking = [m for m in report['matches'] if m['vulnerability']['severity'] in ('High', 'Critical')
            and m['vulnerability'].get('fix', {}).get('state') == 'fixed']
print(f"Scan DB: {db['built']}; matches: {len(report['matches'])}; fixable high/critical: {len(blocking)}")
for m in blocking:
    print(m['artifact']['name'], m['artifact']['version'], m['vulnerability']['id'])
if blocking:
    raise SystemExit('Fixable high/critical vulnerabilities block publication')
print('Other matches remain in the full report for vendor/reachability review; this is not a CVE-free claim.')
PY
