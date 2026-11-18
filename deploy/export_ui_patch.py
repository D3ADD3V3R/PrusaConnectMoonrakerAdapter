"""Export the maintained frontend changes against the pinned Prusa checkout."""

from pathlib import Path
import difflib
import subprocess


ROOT = Path(__file__).resolve().parents[1]
CORE = ROOT / 'upstream/prusa-connect'
TARGET = ROOT / 'deploy/rpi/upstream.patch'

tracked = subprocess.check_output(['git', '-C', str(CORE), 'diff', '--binary', 'HEAD', '--', 'frontend/src', 'docs/changes'])
files = subprocess.check_output(['git', '-C', str(CORE), 'ls-files', '--others', '--exclude-standard', '--', 'frontend/src', 'docs/changes']).decode().splitlines()
parts = [tracked.decode().replace('\r\n', '\n')]
for relative in sorted(files):
    path = CORE / relative
    lines = path.read_text(encoding='utf-8').splitlines(keepends=True)
    parts.append(f'diff --git a/{relative} b/{relative}\nnew file mode 100644\n')
    parts.extend(difflib.unified_diff([], lines, fromfile='/dev/null', tofile=f'b/{relative}'))
TARGET.write_bytes(''.join(parts).encode('utf-8'))
print(f'Exported {TARGET.relative_to(ROOT)} with {len(files)} new files')
