"""Install the core, operator configuration, profiles and PCMA asset together."""

import fcntl
import getpass
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shlex
import signal
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


ASSET_TYPE = 'com.github.aux.d3add3v3r.prusaconnectmoonrakeradapter/pcma-tools'
SETUP_TYPE = 'com.github.aux.d3add3v3r.prusaconnectmoonrakeradapter.setup/pcma-setup'


def run(*args, **kwargs):
    kwargs.setdefault('check', True)
    return subprocess.run([str(arg) for arg in args], **kwargs)


def output(*args):
    return run(*args, capture_output=True, text=True).stdout.strip()


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def installation_path(target, *parts):
    path = target.joinpath(*parts)
    if path.is_symlink() or not path.resolve().is_relative_to(target):
        raise RuntimeError(f'Installation path escapes its directory or is a symlink: {path}')
    return path


def api_request(health, path, token):
    base = health.split('/api/v1/health')[0] if '/api/v1/health' in health else health.rsplit('/', 1)[0]
    headers = {'Authorization': 'Bearer ' + token} if token else {}
    request = urllib.request.Request(base + '/api/v1/' + path, headers=headers)
    with urllib.request.urlopen(request, timeout=3) as response:
        return json.load(response)


def access_token(health):
    token = os.environ.get('INSTALL_PC_TOKEN', '')
    try:
        api_request(health, 'resources?kind=asset', token)
    except urllib.error.HTTPError as error:
        if error.code not in (401, 403):
            raise
        if not sys.stdin.isatty():
            raise RuntimeError('Resource read access requires INSTALL_PC_TOKEN') from None
        token = getpass.getpass('PC API token with permission to view resources: ')
        api_request(health, 'resources?kind=asset', token)
    return token


def wait_asset(health, token):
    deadline = time.monotonic() + float(os.environ.get('HEALTH_TIMEOUT_SECONDS', '45'))
    while True:
        try:
            resource = api_request(health, 'resources/asset/pcma-tools', token)
            status = resource.get('status') or {}
            if (resource.get('type') == ASSET_TYPE and resource.get('enabled')
                    and status.get('state') == 'ready' and status.get('serving')
                    and (resource.get('ui') or {}).get('entry_url')):
                return
        except (OSError, ValueError):
            pass
        if time.monotonic() >= deadline:
            raise RuntimeError('PCMA asset did not become ready with a published UI entry')
        time.sleep(0.3)


def wait_setup(health, token):
    deadline = time.monotonic() + float(os.environ.get('HEALTH_TIMEOUT_SECONDS', '45'))
    while True:
        try:
            resource = api_request(health, 'resources/sink/pcma-setup', token)
            status = resource.get('status') or {}
            if resource.get('type') == SETUP_TYPE and status.get('state') == 'ready' and status.get('serving'):
                return
        except (OSError, ValueError):
            pass
        if time.monotonic() >= deadline:
            raise RuntimeError('PCMA setup service did not become ready')
        time.sleep(0.3)


def ask(title, default=''):
    if not sys.stdin.isatty():
        raise RuntimeError(title + ': terminal input required; use the documented INSTALL_* settings')
    value = input(f'{title}' + (f' [{default}]' if default else '') + ': ').strip()
    return value or default


def service_value(service, key):
    return output('systemctl', 'show', service, '-p', key, '--value')


def config_path(target, service):
    explicit = None
    command = service_value(service, 'ExecStart')
    match = re.search(r'argv\[\]=(.*?)\s;\s', command)
    args = shlex.split(match.group(1)) if match else []
    for index, arg in enumerate(args):
        if arg.startswith(('--config=', '-config=')):
            explicit = arg.split('=', 1)[1]
        elif arg in ('--config', '-config'):
            if index + 1 >= len(args):
                raise RuntimeError('Service --config has no path')
            explicit = args[index + 1]
    # The core default is config/config.yaml. A differently named file needs
    # an explicit service argument; choosing it here would not change the service.
    result = Path(explicit or 'config/config.yaml')
    if not result.is_absolute():
        result = target / result
    override = os.environ.get('INSTALL_CONFIG')
    if override:
        chosen = Path(override)
        if not chosen.is_absolute():
            chosen = target / chosen
        if chosen.resolve() != result.resolve():
            raise RuntimeError('INSTALL_CONFIG differs from the configuration used by the service')
    if result.is_symlink() or not result.is_file():
        raise RuntimeError(f'Active configuration is missing or a symlink: {result}')
    return result.resolve()


def inspect(helper, config, target):
    return json.loads(output(helper, '-config', config, '-work', target))


def resource_rows(info, overlay):
    rows = {(row['kind'], row['name']): row for row in overlay.get('instances', [])}
    for kind, instances in (info.get('resources') or {}).items():
        for name, entry in (instances or {}).items():
            rows[kind, name] = dict(entry, kind=kind, name=name)
    return list(rows.values())


def selection(info, overlay):
    connectors = info['connectors']
    names = [name for name, row in connectors.items()
             if row.get('type') == 'pcma-moonraker' or row.get('type', '').endswith('-moonraker')]
    legacy = [name for name in names if connectors[name]['type'] != 'pcma-moonraker']
    if legacy:
        print('Existing Moonraker connector names will be retained: ' + ', '.join(legacy))
        consent = os.environ.get('INSTALL_MIGRATE') or ask('Migrate these connector types to pcma-moonraker? yes/no', 'yes')
        if consent.lower() not in ('yes', 'y', 'ja', 'j', '1'):
            raise RuntimeError('Connector migration cancelled before any live change')
    if 'pcma' in connectors and connectors['pcma'].get('type') != 'pcma-moonraker' and 'pcma' not in legacy:
        raise RuntimeError('Connector pcma already belongs to another type')
    if not names:
        names = ['pcma']
    elif 'pcma' not in names:
        names.append('pcma')
    endpoints = overlay.get('endpoints', [])
    choices = {}
    for name in names:
        choices[name] = [row for row in endpoints if row.get('connector') == name]
        if not choices[name] and name == 'pcma':
            choices[name] = [row for row in endpoints if row.get('connector') in names]
        if not choices[name]:
            address = os.environ.get('INSTALL_MOONRAKER_ADDRESS') or ask(f'Moonraker address for {name}')
            if not address:
                raise RuntimeError('A Moonraker address is required to generate the profile')
            choices[name] = [{'address': address, 'options': {'model': 'Klipper'}}]
    return choices


def prepare(root, target, config, stage):
    helper = root / 'bin/pcma-install-config'
    info = inspect(helper, config, target)
    overlay_path = Path(info['overlay'])
    overlay_bytes = overlay_path.read_bytes() if overlay_path.exists() else b''
    overlay = json.loads(overlay_bytes) if overlay_bytes else {'version': 1, 'instances': []}
    if overlay.get('version') != 1:
        raise RuntimeError('Unsupported resource overlay version')
    rows = resource_rows(info, overlay)
    if any(row.get('kind') == 'asset' and row.get('name') == 'pcma-tools'
           for row in overlay.get('instances', [])):
        raise RuntimeError('pcma-tools is managed in the resource overlay; resolve that declaration before installing')
    if any(row.get('kind') == 'sink' and row.get('name') == 'pcma-setup'
           for row in overlay.get('instances', [])):
        raise RuntimeError('pcma-setup is managed in the resource overlay; resolve that declaration before installing')
    materials = [row for row in rows if row.get('type') == 'com.github.aux.prusa3d.materials/materials'
                 and row.get('enabled', True)]
    if len(materials) != 1:
        raise RuntimeError('Exactly one enabled Materials module is required')
    version = materials[0].get('module', {}).get('version')
    if not version:
        service_path = Path(materials[0].get('service', {}).get('path', ''))
        if not service_path.is_absolute():
            service_path = target / service_path
        manifest = service_path.parent / 'module.yaml'
        if manifest.is_file():
            match = re.search(r'^version:\s*[\'\"]?([^\s\'\"]+)', manifest.read_text(), re.MULTILINE)
            version = match.group(1) if match else None
        if not version:
            raise RuntimeError('Cannot determine the installed Materials version')
    if version and not re.fullmatch(r'0\.2\.(?:[5-9]|[1-9][0-9]+)', version):
        raise RuntimeError('PCMA requires Materials >=0.2.5 and <0.3.0')
    choices = selection(info, overlay)
    # The prepared asset is immutable and named by content. Upgrades never
    # modify a bundle the old process may still be serving.
    spec = importlib.util.spec_from_file_location('pcma_bundle', root / 'pcma-ui/prepare-install.py')
    bundle = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(bundle)
    source = root / 'pcma-ui/dist'
    if not (source / 'module.yaml').is_file() or not (source / 'ui/index.js').is_file():
        raise RuntimeError('Built PCMA UI bundle is missing')
    checksum = bundle.digest(source)
    asset_path = installation_path(target, 'modules', 'pcma-tools-' + checksum.split(':')[1])
    asset_staged = stage / 'asset'
    shutil.copytree(source, asset_staged)
    if bundle.digest(asset_staged) != checksum:
        raise RuntimeError('Staged UI checksum differs')
    if asset_path.exists() and bundle.digest(asset_path) != checksum:
        raise RuntimeError('Existing content-addressed asset is damaged')
    profiles = {}
    writes = []
    for index, (name, endpoints) in enumerate(choices.items()):
        generated = []
        uploads = info['connectors'].get(name, {}).get('config', {}).get('uploads', False)
        for number, endpoint in enumerate(endpoints):
            profile = stage / f'probe-{index}-{number}.json'
            command = [root / 'bin/pcma-profile', '-address', endpoint['address'],
                       '-model', endpoint.get('options', {}).get('model', 'Klipper'), '-output', profile]
            if uploads:
                command.append('-uploads')
            run(*command)
            data = json.loads(profile.read_text())
            for row in data['profiles']:
                for previous in generated:
                    if previous['model'].lower() == row['model'].lower() and previous != row:
                        raise RuntimeError('Printers of one model have different firmware ranges or capabilities; use separate connector instances')
                if row not in generated:
                    generated.append(row)
        content = (json.dumps({'schema': 1, 'generated_by': 'pcma-profile', 'profiles': generated}, sort_keys=True) + '\n').encode()
        profile_path = installation_path(target, 'profiles', 'pcma-' + hashlib.sha256(content).hexdigest() + '.json')
        profiles[name] = str(profile_path)
        writes.append((profile_path, content, 0o600))
    # The stock Add a Printer form addresses pcma. Retain old instance names
    # as well, so existing endpoint and device identities do not move.
    if 'pcma' not in profiles:
        profiles['pcma'] = next(iter(profiles.values()))
    setup_binary = root / 'bin/pcma-setup_linux_arm64'
    setup_bytes = setup_binary.read_bytes()
    setup_hash = sha(setup_binary)
    setup_path = installation_path(target, 'modules', 'pcma-setup-' + setup_hash)
    writes.append((setup_path / 'pcma-setup_linux_arm64', setup_bytes, 0o755))
    for name in ('LICENCE.md', 'NOTICE.md'):
        writes.append((setup_path / name, (root / name).read_bytes(), 0o644))
    setup_declaration = {
        'type': SETUP_TYPE, 'enabled': True, 'runtime': 'service',
        'module': {'version': '0.3.5', 'title': 'PCMA Klipper setup',
                   'path': 'github.com/d3add3v3r/prusaconnectmoonrakeradapter/setup',
                   'options': [{'key': 'addresses', 'type': 'string', 'default': '{}',
                                'summary': 'JSON map of connector names to approved Moonraker addresses.'}]},
        'service': {'path': str(setup_path), 'checksum': 'sha256:' + setup_hash,
                    'contract_major': 1, 'checks': ['configure'], 'memory_mib': 32},
        'config': {'addresses': json.dumps({name: sorted({endpoint['address'] for endpoint in endpoints}) for name,endpoints in choices.items()},sort_keys=True)},
    }
    plan = stage / 'plan.json'
    plan.write_text(json.dumps({'profiles': profiles, 'setup': setup_declaration, 'asset': {
        'type': ASSET_TYPE, 'enabled': True, 'required': False,
        'asset': {'path': str(asset_path), 'checksum': checksum},
    }}))
    staged_config = stage / 'config.yaml'
    run(helper, '-config', config, '-work', target, '-plan', plan, '-output', staged_config)
    return staged_config, asset_path, asset_staged, writes, overlay_path, overlay_bytes


def atomic_file(destination, data, mode=0o600, owner=None):
    destination.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix='.pcma-', dir=destination.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
            os.fchmod(stream.fileno(), mode)
            if os.geteuid() == 0:
                if owner is None and destination.exists():
                    current = destination.stat()
                    owner = (current.st_uid, current.st_gid)
                os.fchown(stream.fileno(), *(owner or (os.getuid(), os.getgid())))
        os.replace(name, destination)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def main():
    root = Path(os.environ['BUILD_ROOT']).resolve()
    target = Path(os.environ['INSTALL_DIR']).resolve(strict=True)
    service = os.environ['SERVICE']
    health = os.environ['HEALTH_URL']
    binary = target / 'prusa-connect'
    if binary.is_symlink() or not binary.is_file():
        raise RuntimeError('Installed binary must be a regular file')
    if Path(service_value(service, 'WorkingDirectory')).resolve() != target:
        raise RuntimeError('Service working directory differs from INSTALL_DIR')
    if f'path={binary} ;' not in service_value(service, 'ExecStart'):
        raise RuntimeError('Service executable differs from installation target')
    control = ['systemctl'] if os.geteuid() == 0 else ['sudo', '-n', 'systemctl']
    with (target / '.pcma-install.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        run('sha256sum', '--check', 'SHA256SUMS', cwd=root / 'bin')
        config = config_path(target, service)
        original = config.read_bytes()
        print(f'PCMA installation using {config}', flush=True)
        with tempfile.TemporaryDirectory(prefix='.pcma-install-', dir=target) as scratch:
            staged, asset, staged_asset, profiles, overlay, overlay_bytes = prepare(root, target, config, Path(scratch))
            if os.environ.get('INSTALL_PREPARE_ONLY') == '1':
                print('Preparation passed; no live configuration, service or binary changed.')
                return
            token = access_token(health)
            if os.geteuid() != 0:
                if run('sudo', '-n', 'true', check=False).returncode:
                    run('sudo', '-v')
            # Copy the overlay only after the service has stopped writing it.
            was_active = run('systemctl', 'is-active', '--quiet', service, check=False).returncode == 0
            backup = installation_path(target, 'backups', time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()) + f'-{os.getpid()}')
            backup.mkdir(parents=True, mode=0o700)
            stopped = False
            changed = False
            new_files = []
            new_asset = False
            binary_stat = binary.stat()
            config_stat = config.stat()
            overlay_stat = overlay.stat() if overlay.exists() else None
            try:
                stopped = True
                run(*control, 'stop', service)
                if config.read_bytes() != original or (overlay.read_bytes() if overlay.exists() else b'') != overlay_bytes:
                    raise RuntimeError('Configuration changed during preparation; rerun make install')
                shutil.copy2(binary, backup / 'prusa-connect')
                shutil.copy2(config, backup / 'config.yaml')
                if overlay.exists():
                    shutil.copy2(overlay, backup / 'resources.json')
                if not asset.exists():
                    asset.parent.mkdir(parents=True, exist_ok=True)
                    os.replace(staged_asset, asset)
                    new_asset = True
                for path, content, mode in profiles:
                    if path.exists():
                        if path.read_bytes() != content:
                            raise RuntimeError('Existing content-addressed profile differs')
                    else:
                        installed_owner = binary.stat()
                        atomic_file(path, content, mode, owner=(installed_owner.st_uid, installed_owner.st_gid))
                        new_files.append(path)
                changed = True
                atomic_file(config, staged.read_bytes(), config.stat().st_mode & 0o777)
                atomic_file(binary, (root / 'bin/prusa-connect-pcma').read_bytes(), 0o755)
                run(*control, 'start', service)
                version = (root / 'bin/VERSION').read_text().strip()
                run('python3', root / 'scripts/health.py', health, version)
                wait_asset(health, token)
                wait_setup(health, token)
                pid = service_value(service, 'MainPID')
                if sha(Path('/proc') / pid / 'exe') != sha(root / 'bin/prusa-connect-pcma'):
                    raise RuntimeError('Running binary hash differs from candidate')
            except BaseException:
                if stopped:
                    run(*control, 'stop', service, check=False)
                if changed:
                    atomic_file(config, (backup / 'config.yaml').read_bytes(), config_stat.st_mode & 0o777,
                                owner=(config_stat.st_uid, config_stat.st_gid))
                    atomic_file(binary, (backup / 'prusa-connect').read_bytes(), binary_stat.st_mode & 0o777,
                                owner=(binary_stat.st_uid, binary_stat.st_gid))
                    if (backup / 'resources.json').exists():
                        saved = backup / 'resources.json'
                        atomic_file(overlay, saved.read_bytes(), overlay_stat.st_mode & 0o777,
                                    owner=(overlay_stat.st_uid, overlay_stat.st_gid))
                    elif overlay.exists():
                        overlay.unlink()
                for path in new_files:
                    path.unlink()
                if new_asset:
                    shutil.rmtree(asset)
                if stopped and was_active:
                    run(*control, 'start', service)
                print(f'Installation failed. Previous configuration and binary restored. Backup: {backup}', file=sys.stderr)
                raise
            print(f'Installed {version}\nConfiguration: {config}\nPCMA tab: enabled\nCapability profiles: generated and registered\nBackup: {backup}')
            print('Printer macros and automatic print-start settings were not changed.')


if __name__ == '__main__':
    def interrupted(_signum, _frame):
        raise InterruptedError('Installation interrupted')

    signal.signal(signal.SIGTERM, interrupted)
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print(f'PCMA installation stopped: {error}', file=sys.stderr)
        sys.exit(1)
