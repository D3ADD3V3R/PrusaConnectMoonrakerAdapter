"""Verify the PCMA namespace can read/write records across a real host restart."""

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile

from pc_smoke import ROOT, available_port, executable, request, wait_for


def main():
    scratch = ROOT / '.test-run'
    scratch.mkdir(exist_ok=True)
    spec = importlib.util.spec_from_file_location('pcma_bundle', ROOT / 'pcma-ui/prepare-install.py')
    bundle = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(bundle)
    with tempfile.TemporaryDirectory(prefix='records-smoke-', dir=scratch) as name:
        work = Path(name)
        asset = work / 'asset'
        (asset / 'ui').mkdir(parents=True)
        # A small asset claims the real namespace, without needing a printer or
        # Materials process. The test exercises the host's durable record door.
        (asset / 'module.yaml').write_text(json.dumps({
            'name': 'pcma-tools', 'kind': 'asset', 'version': '0.3.5',
            'license': 'OCL v1.1 + Micro v1 + NoSaaS v1 + SWAtt v1',
            'title': 'PCMA records fixture', 'summary': 'Tests durable records.',
            'codeowner': 'D3ADD3V3R',
            'ui': {'entry': 'ui/index.js', 'library': '>=0.1.0 <1.0.0'},
        }), encoding='utf-8')
        (asset / 'ui/index.js').write_text('export {};\n', encoding='utf-8')
        base = f'http://127.0.0.1:{available_port()}'
        config = work / 'config.yaml'
        config.write_text(json.dumps({
            'core': {'api': {'addr': base.removeprefix('http://')},
                     'overlay': str(work / 'resources.json'),
                     'authz': {'database': str(work / 'authz.db')},
                     'auth': {'setup_code': 'PCMA-RECORD-SMOKE'},
                     'discovery': {'enabled': False}},
            'resources': {
                'db': {'pcma-records': {'type': 'sqlite', 'enabled': True, 'required': True,
                                       'config': {'path': str(work / 'records.sqlite')}}},
                'asset': {'pcma-tools': {
                    'type': 'com.github.aux.d3add3v3r.prusaconnectmoonrakeradapter/pcma-tools',
                    'enabled': True, 'asset': {'path': str(asset), 'checksum': bundle.digest(asset)},
                }},
            },
        }), encoding='utf-8')
        route = '/api/v1/modules/com.github.aux.d3add3v3r.prusaconnectmoonrakeradapter/state'
        value = {'status': 'requested', 'material': 'PLA', 'color': 'C_FF8000'}
        process = None
        with (work / 'process.log').open('w+', encoding='utf-8') as log:
            try:
                for attempt in range(2):
                    process = subprocess.Popen(
                        [str(executable('prusa-connect-pcma')), 'core', '--config', str(config)],
                        cwd=work, stdout=log, stderr=log,
                        creationflags=subprocess.CREATE_NO_WINDOW if os.name == 'nt' else 0)
                    wait_for(lambda: request(base, '/api/v1/health'), [process])
                    if attempt == 0:
                        request(base, '/api/v1/setup', 'POST',
                                {'mode': 'open', 'code': 'PCMA-RECORD-SMOKE'})
                    wait_for(lambda: request(base, route), [process])
                    if attempt == 0:
                        assert request(base, route)['items'] == []
                        request(base, route + '/change-smoke', 'PUT', {'value': value})
                    assert request(base, route + '/change-smoke')['value'] == value
                    process.terminate()
                    process.wait(timeout=15)
                    process = None
                print('PASS PCMA record list, write, read and persistence across host restart')
            except BaseException:
                log.flush()
                log.seek(0)
                print(log.read())
                raise
            finally:
                if process is not None:
                    process.terminate()
                    try:
                        process.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=5)


if __name__ == '__main__':
    main()
