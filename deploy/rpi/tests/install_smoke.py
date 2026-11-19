"""Verify integrated installation, preflight and rollback with an isolated service."""

import hashlib
import importlib.util
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def check_installation(fail_health, fail_profile=False, fail_ui=False, auth=None, fail_records=False):
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
        config = target_dir / 'config/config.yaml'
        original_config = {'core': {'connectors': {
            'existing': {'type': 'previous-moonraker', 'config': {'uploads': True, 'starts': False}},
            'xbuddy': {'type': 'xbuddy'}}, 'site': 'keep-site'},
            'resources': {'storage': {'keep': {'type': 'filesystem', 'config': {'root': './keep'}}}}}
        config.write_text(json.dumps(original_config))
        overlay = target_dir / 'config/config.d/resources.json'
        overlay.parent.mkdir()
        overlay.write_text(json.dumps({'version': 1, 'identities': {'keep': {'uid': 'unchanged'}},
            'instances': [{'kind': 'sink', 'name': 'materials',
                'type': 'com.github.aux.prusa3d.materials/materials', 'enabled': True,
                'module': {'version': '0.2.5'}}],
            'endpoints': [{'name': 'printer', 'connector': 'existing', 'address': 'localhost:7125',
                           'options': {'model': 'Klipper'}}]}))
        if fail_profile == 'overlap':
            data = json.loads(overlay.read_text())
            data['endpoints'].append({'name': 'second', 'connector': 'existing', 'address': 'localhost:7126'})
            overlay.write_text(json.dumps(data))
        if fail_profile == 'setup_collision':
            data = json.loads(overlay.read_text())
            data['instances'].append({'kind':'sink','name':'pcma-setup','type':'org.example.other/worker','enabled':True})
            overlay.write_text(json.dumps(data))
        original_overlay = overlay.read_bytes()
        (work / 'bin').mkdir()
        candidate = work / 'bin/prusa-connect-pcma'
        shutil.copy2('/usr/bin/sleep', candidate)
        candidate_hash = digest(candidate)
        (work / 'bin/SHA256SUMS').write_text(candidate_hash + '  prusa-connect-pcma\n')
        (work / 'bin/VERSION').write_text('installation-test\n')
        (work / 'scripts').mkdir()
        shutil.copy2(ROOT / 'scripts/health.py', work / 'scripts/health.py')
        shutil.copy2(ROOT / 'scripts/install.py', work / 'scripts/install.py')
        shutil.copy2(ROOT / 'bin/pcma-install-config', work / 'bin/pcma-install-config')
        shutil.copy2('/usr/bin/true', work / 'bin/pcma-setup_linux_arm64')
        for filename in ('LICENCE.md', 'NOTICE.md'):
            shutil.copy2(ROOT / filename, work / filename)
        (work / 'pcma-ui/dist/ui').mkdir(parents=True)
        shutil.copy2(ROOT / 'pcma-ui/prepare-install.py', work / 'pcma-ui/prepare-install.py')
        (work / 'pcma-ui/dist/module.yaml').write_text('name: pcma-tools\nkind: asset\nversion: 0.3.4\n')
        (work / 'pcma-ui/dist/ui/index.js').write_text('export const test = true;\n')
        (work / 'bin/pcma-profile').write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
if os.environ.get('FAKE_FAIL_PROFILE') == '1': sys.exit(1)
firmware = '0.13.0'
if os.environ.get('FAKE_PROFILE_OVERLAP') == '1':
    firmware = '0.13.0-828-gdef' if 'localhost:7126' in sys.argv else '0.13.0-827-gabc'
pathlib.Path(sys.argv[sys.argv.index('-output')+1]).write_text(json.dumps({
    'schema': 1, 'profiles': [{'model': 'Klipper', 'firmware': {'from': firmware, 'to': '0.13.1-0'},
    'capabilities': {'upload': True} if '-uploads' in sys.argv else {}}]}))
''')
        (work / 'bin/pcma-profile').chmod(0o755)
        commands = work / 'fake-commands'
        commands.mkdir()
        events = work / 'events.txt'

        class Health(BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path.startswith(('/api/v1/resources', '/api/v1/modules')) and auth:
                    if auth == 'denied' or self.headers.get('Authorization') != 'Bearer isolated-test-token':
                        self.send_response(403)
                        self.end_headers()
                        self.wfile.write(b'{}')
                        return
                if self.path.endswith('/state'):
                    self.send_response(501 if fail_records else 200)
                    self.end_headers()
                    self.wfile.write(b'{}' if fail_records else b'{"items": []}')
                    return
                self.send_response(200)
                self.end_headers()
                if self.path.split('?')[0] == '/api/v1/resources':
                    self.wfile.write(b'{"items": []}')
                    return
                if self.path == '/api/v1/resources/asset/pcma-tools':
                    self.wfile.write(json.dumps({
                        'type': 'com.github.aux.d3add3v3r.prusaconnectmoonrakeradapter/pcma-tools',
                        'enabled': True, 'status': {'state': 'failed' if fail_ui else 'ready', 'serving': not fail_ui},
                        'ui': None if fail_ui else {'entry_url': '/ui/index.js'},
                    }).encode())
                    return
                if self.path == '/api/v1/resources/sink/pcma-setup':
                    self.wfile.write(json.dumps({
                        'type': 'com.github.aux.d3add3v3r.prusaconnectmoonrakeradapter.setup/pcma-setup',
                        'status': {'state': 'ready', 'serving': True},
                    }).encode())
                    return
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
    if [[ "$1" == start && "$FAKE_MUTATE_OVERLAY" == 1 ]] && [[ $(grep -c '^start$' "$FAKE_EVENTS") == 1 ]]; then
        printf '{"version":1,"instances":[],"new_core_state":true}' > "$FAKE_OVERLAY"
    fi
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
            environment.pop('INSTALL_PC_TOKEN', None)
            if auth == 'required':
                environment['INSTALL_PC_TOKEN'] = 'isolated-test-token'
            environment.update({
                'PATH': str(commands) + os.pathsep + environment['PATH'],
                'BUILD_ROOT': str(work), 'INSTALL_DIR': str(target_dir),
                'SERVICE': 'isolated-test.service',
                'HEALTH_URL': f'http://127.0.0.1:{server.server_port}/health',
                'HEALTH_TIMEOUT_SECONDS': '1.5',
                'FAKE_INSTALL_DIR': str(target_dir), 'FAKE_PID': str(running.pid),
                'FAKE_EVENTS': str(events),
                'FAKE_OVERLAY': str(overlay), 'FAKE_MUTATE_OVERLAY': '1' if fail_health or fail_ui or fail_records else '0',
                'FAKE_FAIL_PROFILE': '1' if fail_profile is True else '0',
                'FAKE_PROFILE_OVERLAP': '1' if fail_profile == 'overlap' else '0',
                'INSTALL_MIGRATE': '1',
            })
            result = subprocess.run(['bash', str(ROOT / 'scripts/install.sh')],
                                    env=environment, stdin=subprocess.DEVNULL,
                                    capture_output=True, text=True, timeout=15)
            if fail_profile or auth == 'denied':
                assert result.returncode != 0, result.stdout + result.stderr
                if fail_profile == 'overlap':
                    assert 'different firmware ranges' in result.stderr, result.stderr
                if fail_profile == 'setup_collision':
                    assert 'pcma-setup is managed in the resource overlay' in result.stderr, result.stderr
                if auth == 'denied':
                    assert 'Resource read access requires INSTALL_PC_TOKEN' in result.stderr, result.stderr
                assert digest(target) == old_hash
                assert json.loads(config.read_text()) == original_config
                assert overlay.read_bytes() == original_overlay
                assert not events.exists(), 'service touched before profile was ready'
                return
            expected = old_hash if fail_health or fail_ui or fail_records else candidate_hash
            assert digest(target) == expected, result.stdout + result.stderr
            assert (result.returncode != 0) == (fail_health or fail_ui or fail_records), result.stdout + result.stderr
            if fail_records:
                assert 'record store did not become readable' in result.stderr
            backups = list((target_dir / 'backups').iterdir())
            assert len(backups) == 1 and digest(backups[0] / 'prusa-connect') == old_hash
            assert sentinel.read_text() == 'existing configuration'
            assert overlay.read_bytes() == original_overlay
            if fail_health or fail_ui or fail_records:
                assert json.loads(config.read_text()) == original_config
                assert not list((target_dir / 'modules').glob('pcma-tools-*'))
                assert not list((target_dir / 'profiles').glob('pcma-*.json'))
                assert not list((target_dir / 'modules').rglob('*linux_arm64'))
            else:
                info = json.loads(subprocess.check_output([str(work / 'bin/pcma-install-config'),
                    '-config', str(config), '-work', str(target_dir)]))
                assert info['connectors']['existing']['type'] == 'pcma-moonraker'
                assert info['connectors']['existing']['config']['starts'] is False
                assert info['connectors']['existing']['config']['uploads'] is True
                assert info['connectors']['pcma']['type'] == 'pcma-moonraker'
                assert info['connectors']['xbuddy']['type'] == 'xbuddy'
                asset = info['resources']['asset']['pcma-tools']
                assert asset['enabled'] and not asset['required'] and Path(asset['asset']['path']).is_dir()
                setup = info['resources']['sink']['pcma-setup']
                records = info['resources']['db']['pcma-records']
                assert records == {'type': 'sqlite', 'enabled': True, 'required': True,
                                   'config': {'path': str(target_dir / 'config/pcma-records.sqlite')}}
                assert setup['service']['checks'] == ['configure']
                assert setup['service']['memory_mib'] == 32
                assert json.loads(setup['config']['addresses']) == {'existing':['localhost:7125'],'pcma':['localhost:7125']}
                first_config = config.read_bytes()
                if auth == 'required':
                    credential = target_dir / '.pcma-installer-token.json'
                    assert credential.stat().st_mode & 0o777 == 0o600
                    assert json.loads(credential.read_text())['token'] == 'isolated-test-token'
                    assert 'isolated-test-token' not in result.stdout + result.stderr
                    # An update must work without the one-time input or a terminal.
                    environment.pop('INSTALL_PC_TOKEN')
                repeated = subprocess.run(['bash', str(ROOT / 'scripts/install.sh')],
                    env=environment, stdin=subprocess.DEVNULL,
                    capture_output=True, text=True, timeout=15)
                assert repeated.returncode == 0, repeated.stdout + repeated.stderr
                assert config.read_bytes() == first_config
            actions = events.read_text().splitlines()
            assert actions == ['stop', 'start', 'stop', 'start'], actions
        finally:
            running.terminate()
            running.wait(timeout=5)
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


def check_configuration_rules():
    spec = importlib.util.spec_from_file_location('pcma_installer', ROOT / 'scripts/install.py')
    installer = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(installer)
    row = {'kind': 'sink', 'name': 'materials', 'type': 'com.github.aux.prusa3d.materials/materials', 'enabled': True}
    info = {'resources': {'sink': {'materials': dict(row, enabled=False)}}}
    rows = installer.resource_rows(info, {'instances': [row]})
    assert len(rows) == 1 and rows[0]['enabled'] is False, 'operator file must shadow the overlay'
    with tempfile.TemporaryDirectory(prefix='config-path-', dir=ROOT / '.test-run') as name:
        target = Path(name)
        existing = {'kind': 'db', 'name': 'custom', 'type': 'postgres',
                    'config': {'dsn': 'operator-managed'}}
        assert installer.record_backend([existing], target) is None
        assert installer.record_backend([dict(existing, enabled=False)], target)['type'] == 'sqlite'
        for rows in ([existing, dict(existing, name='second')],
                     [dict(existing, name='pcma-records', enabled=False)]):
            try:
                installer.record_backend(rows, target)
            except RuntimeError:
                pass
            else:
                raise AssertionError('ambiguous or explicitly disabled record configuration accepted')
        config = target / 'config.yml'
        config.write_text('core: {}')
        command = f'{{ path={target}/prusa-connect ; argv[]={target}/prusa-connect --config config.yml ; }}'
        with patch.object(installer, 'service_value', return_value=command), patch.dict(os.environ, {'INSTALL_CONFIG': ''}):
            assert installer.config_path(target, 'fake') == config
        with patch.object(installer, 'service_value', return_value=command), patch.dict(os.environ, {'INSTALL_CONFIG': 'wrong.yaml'}):
            try:
                installer.config_path(target, 'fake')
            except RuntimeError:
                pass
            else:
                raise AssertionError('configuration ignored by service was accepted')

    with tempfile.TemporaryDirectory(prefix='token-cache-', dir=ROOT / '.test-run') as name:
        target = Path(name).resolve()
        health = 'http://127.0.0.1:8080/api/v1/health'
        credential = target / '.pcma-installer-token.json'
        saved = json.dumps({'health_url': health, 'token': 'isolated-saved-token'}).encode()
        installer.atomic_file(credential, saved)
        with patch.dict(os.environ, {'INSTALL_PC_TOKEN': ''}), patch.object(installer, 'api_request') as api:
            # Credentials for one server must never be sent to another.
            assert installer.access_token(health + '/different', target) == ''
            assert api.call_args.args[2] == ''
            assert credential.read_bytes() == saved
            api.side_effect = installer.urllib.error.HTTPError(health, 401, 'Denied', {}, None)
            with patch.object(installer.sys.stdin, 'isatty', return_value=False):
                try:
                    installer.access_token(health, target)
                except RuntimeError as error:
                    assert 'no valid saved installer token' in str(error)
                else:
                    raise AssertionError('revoked cached token accepted')
            assert credential.read_bytes() == saved
            api.side_effect = None
            credential.chmod(0o644)
            api.reset_mock()
            try:
                installer.access_token(health, target)
            except RuntimeError as error:
                assert 'private' in str(error)
            else:
                raise AssertionError('publicly readable token accepted')
            api.assert_not_called()


if __name__ == '__main__':
    check_installation(False)
    check_installation(True)
    check_installation(False, fail_ui=True)
    check_installation(False, fail_records=True)
    check_installation(False, auth='required')
    check_installation(False, auth='denied')
    check_installation(False, fail_profile=True)
    check_installation(False, fail_profile='overlap')
    check_installation(False, fail_profile='setup_collision')
    check_configuration_rules()
    print('PASS install, repeat install, binary/config/overlay rollback, failed-probe isolation, overlapping profiles and YAML precedence')
