#!/usr/bin/env bash
# Core functionality and retained non-root installs; actual OpenShell policy is
# exercised separately in the deployment repository's local Kubernetes pilot.
set -euo pipefail
if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <local-image>" >&2
  exit 2
fi
volume=$(docker volume create)
trap 'docker volume rm "$volume" >/dev/null' EXIT
run=(docker run --rm --platform linux/amd64 --memory 512m --cpus 0.5 --cap-drop ALL --security-opt no-new-privileges --mount "type=volume,src=$volume,dst=/sandbox")
# Commands expand inside the container, not on the host.
# shellcheck disable=SC2016
"${run[@]}" "$1" sh -ec '
  test "$(id -u)" = 10001
  for tool in curl jq sqlite3 unzip; do command -v "$tool"; done
  # Reproduce interrupted uv initialization, then concurrent first use.
  mkdir -p /sandbox/.venv/bin
  ln -s /usr/local/bin/python3.12 /sandbox/.venv/bin/python3
  python -c "import sys; assert sys.prefix == '\''/sandbox/.venv'\''" &
  first=$!
  python -c "import csv, json, sqlite3, zipfile; import sys; assert sys.prefix == '\''/sandbox/.venv'\''"
  wait "$first"
  python -c "import importlib.util, os; assert importlib.util.find_spec('\''pip'\'') is None; assert os.environ['\''UV_NO_BUILD'\''] == '\''1'\''; assert os.environ['\''UV_PYTHON_DOWNLOADS'\''] == '\''never'\''"
  uv pip install pypdf==6.11.0 openpyxl==3.1.5
  python -c "from openpyxl import Workbook, load_workbook; w=Workbook(); w.active.append(['\''value'\'', 42]); w.save('\''/sandbox/check.xlsx'\''); assert load_workbook('\''/sandbox/check.xlsx'\'').active['\''B1'\''].value == 42"
  test ! -w /usr/local/lib/python3.12/site-packages
'
"${run[@]}" --network none "$1" sh -lc '
  python -c "from pypdf import PdfWriter; from openpyxl import load_workbook; assert load_workbook('\''/sandbox/check.xlsx'\'').active['\''B1'\''].value == 42; p=PdfWriter(); p.add_blank_page(width=72, height=72); p.write('\''/sandbox/check.pdf'\'')"
  uv pip check
'
