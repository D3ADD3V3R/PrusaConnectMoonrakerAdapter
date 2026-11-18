"""Verify atomic binary installation and rollback using an isolated fake service."""

import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading


ROOT = Path(__file__).resolve().parents[1]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def check_installation(fail_health):
    scratch = ROOT / '.test-run'
    scratch.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='install-check-', dir=scratch) as name:
        work = Path(name)
        target_dir = work / 'installation'
        target_dir.mkdir()
        target = target_dir / 'prusa-connect'
        shutil.copy2('/usr/bin/true', target)
        old_hash = digest(target)
        (target_dir / 'config').mkdir()
        sentinel = target_dir / 'config/keep.txt'
        sentinel.write_text('existing configuration', encoding='utf-8')
        (work / 'bin').mkdir()
        candidate = work / 'bin/prusa-connect-pcma'
        shutil.copy2('/usr/bin/sleep', candidate)
        candidate_hash = digest(candidate)
        (work / 'bin/SHA256SUMS').write_text(candidate_hash + '  prusa-connect-pcma\n')
        (work / 'bin/VERSION').write_text('installation-test\n')
        (work / 'scripts').mkdir()
        shutil.copy2(ROOT / 'scripts/health.py', work / 'scripts/health.py')
        commands = work / 'fake-commands'
        commands.mkdir()
        events = work / 'events.txt'

        class Health(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                version = 'wrong-version' if fail_health else 'installation-test'
                self.wfile.write(('{' + f'"status":"ok","version":"{version}"' + '}').encode())

            def log_message(self, *_args):
                pass

        server = ThreadingHTTPServer(('127.0.0.1', 0), Health)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        running = subprocess.Popen(['/usr/bin/sleep', '60'])
        try:
            (commands / 'systemctl').write_text('''#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == show ]]; then
    case "$4" in
        WorkingDirectory) printf '%s\\n' "$FAKE_INSTALL_DIR" ;;
        ExecStart) printf '{ path=%s/prusa-connect ; }\\n' "$FAKE_INSTALL_DIR" ;;
        MainPID) printf '%s\\n' "$FAKE_PID" ;;
        *) exit 1 ;;
    esac
elif [[ "$1" == is-active ]]; then
    exit 0
else
    printf '%s\\n' "$1" >> "$FAKE_EVENTS"
fi
''', encoding='utf-8')
            (commands / 'sudo').write_text('''#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == -n ]]
shift
exec "$@"
''', encoding='utf-8')
            for script in commands.iterdir():
                script.chmod(0o755)
            environment = os.environ.copy()
            environment.update({
                'PATH': str(commands) + os.pathsep + environment['PATH'],
                'BUILD_ROOT': str(work), 'INSTALL_DIR': str(target_dir),
                'SERVICE': 'isolated-test.service',
                'HEALTH_URL': f'http://127.0.0.1:{server.server_port}/health',
                'HEALTH_TIMEOUT_SECONDS': '1.5',
                'FAKE_INSTALL_DIR': str(target_dir), 'FAKE_PID': str(running.pid),
                'FAKE_EVENTS': str(events),
            })
            result = subprocess.run(['bash', str(ROOT / 'scripts/install.sh')],
                                    env=environment, capture_output=True, text=True, timeout=15)
            expected = old_hash if fail_health else candidate_hash
            assert digest(target) == expected, result.stdout + result.stderr
            assert (result.returncode != 0) == fail_health, result.stdout + result.stderr
            backups = list((target_dir / 'backups').iterdir())
            assert len(backups) == 1 and digest(backups[0]) == old_hash
            assert sentinel.read_text() == 'existing configuration'
            actions = events.read_text().splitlines()
            assert actions == (['restart', 'stop', 'start'] if fail_health else ['restart']), actions
        finally:
            running.terminate()
            running.wait(timeout=5)
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


if __name__ == '__main__':
    check_installation(False)
    check_installation(True)
    print('PASS installation and automatic rollback with isolated service')
