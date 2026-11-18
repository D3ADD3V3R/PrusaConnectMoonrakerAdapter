"""Exercise the compiled PC connector through its normal loopback API."""

import json
import argparse
import gzip
import hashlib
import os
from pathlib import Path
import socket
import re
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[1]

def executable(name):
    return ROOT / 'bin' / (name + ('.exe' if os.name == 'nt' else ''))


def available_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


def request(base, route, method='GET', body=None, headers=None):
    data = None if body is None else json.dumps(body).encode()
    request_headers = {'Content-Type': 'application/json'}
    request_headers.update(headers or {})
    query = urllib.request.Request(base + route, data, request_headers, method=method)
    with urllib.request.urlopen(query, timeout=5) as response:
        return json.load(response)


def wait_for(check, processes, timeout=40):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        for process in processes:
            if process.poll() is not None:
                raise RuntimeError(f'Local test process exited with {process.returncode}')
        try:
            result = check()
            if result:
                return result
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.2)
    raise TimeoutError('Local PC smoke check did not settle')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--require-ui', action='store_true')
    options = parser.parse_args()
    test_root = ROOT / '.test-run'
    test_root.mkdir(exist_ok=True)
    binary = executable('prusa-connect-pcma')
    simulator = executable('moonraker-sim')
    if not binary.is_file() or not simulator.is_file():
        raise RuntimeError('Run dev.ps1 build before the smoke check')
    flags = subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0
    with tempfile.TemporaryDirectory(prefix='pc-smoke-', dir=test_root) as work_name:
        work = Path(work_name)
        api_port = available_port()
        sim_port = available_port()
        while sim_port == api_port:
            sim_port = available_port()
        base = f'http://127.0.0.1:{api_port}'
        storage = work / 'storage'
        storage.mkdir()
        original = b'G90\n; filament_type = PLA\n; filament_colour = #FF8000\n'
        source = storage / 'source.gcode'
        source.write_bytes(original)
        prepared = storage / 'job-1.gcode'
        metadata = json.loads(subprocess.check_output([
            str(executable('pcma-prepare')), '-input', str(source), '-output', str(prepared),
            '-execution', 'job-1', '-profile', 'ender3', '-profile-version', 'v1',
        ], creationflags=flags))
        fixture = prepared.read_bytes()
        assert metadata['original_sha256'] == hashlib.sha256(original).hexdigest(), metadata
        assert source.read_bytes() == original
        config = {
            'core': {
                'api': {'addr': f'127.0.0.1:{api_port}'},
                'log': {'level': 'info', 'format': 'text'},
                'overlay': str(work / 'resources.json'),
                'authz': {'database': str(work / 'authz.db')},
                'principal': 'operator', 'site': 'pcma-smoke',
                'auth': {'setup_code': 'PCMA-LOCAL-SMOKE'},
                'discovery': {'enabled': False},
                'connectors': {'pcma': {'type': 'pcma-moonraker', 'config': {
                    'uploads': True, 'starts': True, 'state_path': str(work / 'executions.db'),
                    'profile_id': 'ender3', 'profile_version': 'v1', 'materials': ['PLA', 'PETG'],
                }}},
            },
            'resources': {'storage': {'local': {
                'type': 'filesystem', 'enabled': True, 'required': True,
                'config': {'root': str(storage)},
            }}},
        }
        # JSON is valid YAML and avoids a Python YAML dependency.
        config_path = work / 'config.yaml'
        config_path.write_text(json.dumps(config), encoding='utf-8')
        processes = []
        with (work / 'process.log').open('w+', encoding='utf-8') as log:
            try:
                processes.append(subprocess.Popen(
                    [str(simulator), '-addr', f'127.0.0.1:{sim_port}', '-hostname', 'pcma-sim', '-pcma'],
                    cwd=work, stdout=log, stderr=log, creationflags=flags,
                ))
                processes.append(subprocess.Popen(
                    [str(binary), 'core', '--config', str(config_path)],
                    cwd=work, stdout=log, stderr=log, creationflags=flags,
                ))
                wait_for(lambda: request(base, '/api/v1/health'), processes)
                if options.require_ui:
                    with urllib.request.urlopen(base + '/', timeout=5) as response:
                        page = response.read()
                        if response.headers.get('Content-Encoding') == 'gzip':
                            page = gzip.decompress(page)
                    assert b'<html' in page.lower(), 'PC web frontend is missing'
                    asset = re.search(rb'src="(/assets/[^" ]+\.js)"', page)
                    assert asset, 'Frontend entry script is missing'
                    with urllib.request.urlopen(base + asset.group(1).decode(), timeout=5) as response:
                        assert response.status == 200 and response.read(1), 'Frontend asset is unavailable'
                request(base, '/api/v1/setup', 'POST', {'mode': 'open', 'code': 'PCMA-LOCAL-SMOKE'})
                wait_for(lambda: request(base, '/api/v1/storages/local/stat?path=job-1.gcode'), processes)
                request(base, '/api/v1/connector-endpoints/pcma-sim', 'PUT', {
                    'connector': 'pcma', 'address': f'127.0.0.1:{sim_port}', 'enabled': True,
                    'options': {'vendor': 'Creality', 'model': 'Klipper', 'serial': 'PCMA-SIM-001'},
                }, {'If-None-Match': '*'})

                def connected_device():
                    devices = request(base, '/api/v1/devices')['items']
                    return next((item for item in devices if item.get('connected')), None)

                device = wait_for(connected_device, processes)
                assert device['driver'] == 'pcma', device
                assert device['model'] == 'Klipper', device
                assert device['serial'] == 'PCMA-SIM-001', device
                details = request(base, f"/api/v1/devices/{device['id']}")
                assert 'core.command.start_print' in details['capabilities'], details
                assert 'core.feature.file_upload' in details['capabilities'], details
                device_base = f"/api/v1/devices/{device['id']}"

                def listed_file(relative, expected):
                    listing = request(base, device_base + '/files?path=' + relative)
                    return next((item for item in listing.get('items', []) if item['path'] == expected), None)

                wait_for(lambda: listed_file('gcodes', 'gcodes/sample.gcode'), processes)
                sent = request(base, device_base + '/files', 'POST', {
                    'storage': 'local', 'source': 'job-1.gcode', 'path': 'gcodes/pcma/job-1.gcode',
                })
                assert sent['transfer']['state'] == 'done', sent
                item = wait_for(lambda: listed_file('gcodes/pcma', 'gcodes/pcma/job-1.gcode'), processes)
                assert item['size'] == len(fixture), item
                started = request(base, device_base + '/print', 'POST', {'path': 'gcodes/pcma/job-1.gcode'})
                assert started['command']['ok'], started

                def material_pause():
                    state = request(base, device_base)['state']
                    return state if state.get('/pcma/phase') == 'MATERIAL_CHANGE' else None

                state = wait_for(material_pause, processes)
                assert state['/pcma/execution_status'] == 'PRINT_ONGOING', state
                assert state['/pcma/execution_id'] == 'job-1', state
                assert state['/job/id'] == 'pcma-job-1', state
                resumed = request(base, device_base + '/commands', 'POST', {'verb': 'core.command.resume_print'})
                assert not resumed['ok'], resumed
                print(json.dumps({'result': 'PASS', 'driver': device['driver'],
                                  'model': device['model'], 'serial': device['serial'],
                                  'file_listing': 'PASS', 'native_upload': 'PASS',
                                  'native_start': 'PASS', 'material_pause_same_execution': 'PASS'}))
            except BaseException:
                log.flush()
                log.seek(0)
                print(log.read())
                raise
            finally:
                for process in reversed(processes):
                    process.terminate()
                for process in reversed(processes):
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=5)


if __name__ == '__main__':
    main()
