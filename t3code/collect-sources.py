#!/usr/bin/env python3
"""Accompany Debian binaries with their exact corresponding source archives."""
import hashlib
from pathlib import Path
import subprocess

root = Path('/usr/share/source/t3code-bundle')
root.mkdir(parents=True, exist_ok=False)
# Use authenticated Debian indexes; apt verifies downloads against those indexes.
sources = Path('/etc/apt/sources.list.d/debian.sources')
sources.write_text(sources.read_text().replace('Types: deb\n', 'Types: deb deb-src\n'))
subprocess.run(['apt-get', 'update'], check=True)
packages = subprocess.check_output([
    'dpkg-query', '-W', '-f=${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\n',
], text=True)
(root / 'packages.tsv').write_text(packages)
versions = sorted({f'{fields[2]}={fields[3]}' for row in packages.splitlines()
                   if len(fields := row.split('\t')) == 4})
for version in versions:
    subprocess.run(['apt-get', '-y', '--download-only', 'source', version], cwd=root, check=True)
# Include Debian's .dsc and patch/build metadata as well as the upstream archives.
archives = sorted(path for path in root.iterdir() if path.name != 'packages.tsv')
if not any(path.suffix == '.dsc' for path in archives):
    raise SystemExit('No source descriptors downloaded')
(root / 'SHA256SUMS').write_text(''.join(
    f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n' for path in archives
))
sources.write_text(sources.read_text().replace('Types: deb deb-src\n', 'Types: deb\n'))
print(f'Preserved source for {len(versions)} Debian source packages', flush=True)
