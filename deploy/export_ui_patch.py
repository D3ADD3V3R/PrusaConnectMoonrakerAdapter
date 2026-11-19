"""Export the maintained frontend changes against the pinned Prusa checkout."""

from pathlib import Path
import difflib
import subprocess


ROOT = Path(__file__).resolve().parents[1]
CORE = ROOT / 'upstream/prusa-connect'
TARGET = ROOT / 'deploy/rpi/upstream.patch'

scopes = ['frontend/src', 'packages/svelte/src/shell/printer-detail/controls', 'packages/svelte/src/i18n/catalogues.ts', 'packages/svelte/locales', 'packages/typescript/src/generated/operations.ts', 'internal/app/connectors.go', 'internal/app/connectors_test.go', 'internal/transport/http/httpapi/devices.go', 'internal/transport/http/httpapi/budget_test.go', 'internal/transport/http/httpapi/streaming_test.go', 'internal/transport/http/httpapi/openapi.json', 'docs/changes']
tracked = subprocess.check_output(['git', '-C', str(CORE), 'diff', '--binary', 'HEAD', '--', *scopes])
files = subprocess.check_output(['git', '-C', str(CORE), 'ls-files', '--others', '--exclude-standard', '--', *scopes]).decode().splitlines()
parts = [tracked.decode().replace('\r\n', '\n')]
for relative in sorted(files):
    path = CORE / relative
    lines = path.read_text(encoding='utf-8').splitlines(keepends=True)
    parts.append(f'diff --git a/{relative} b/{relative}\nnew file mode 100644\n')
    parts.extend(difflib.unified_diff([], lines, fromfile='/dev/null', tofile=f'b/{relative}'))
TARGET.write_bytes(''.join(parts).encode('utf-8'))
print(f'Exported {TARGET.relative_to(ROOT)} with {len(files)} new files')
