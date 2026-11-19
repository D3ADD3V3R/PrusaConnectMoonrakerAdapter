"""Copy a built PCMA asset into a new directory and print its PC declaration."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil


def digest(directory):
    result = hashlib.sha256()

    def walk(parent):
        for entry in sorted(parent.iterdir(), key=lambda item: item.name):
            if entry.is_symlink():
                raise ValueError('Asset bundles cannot contain symbolic links')
            if entry.is_dir():
                walk(entry)
            elif entry.is_file():
                relative = entry.relative_to(directory).as_posix()
                file_hash = hashlib.sha256(entry.read_bytes()).hexdigest()
                result.update(f'{relative}\0{file_hash}\n'.encode())
            else:
                raise ValueError('Asset bundles contain regular files only')

    walk(directory)
    return 'sha256:' + result.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('destination', type=Path)
    options = parser.parse_args()
    source = Path(__file__).resolve().parent / 'dist'
    destination = options.destination.expanduser().resolve()
    if not (source / 'module.yaml').is_file() or not (source / 'ui/index.js').is_file():
        parser.error('Build pcma-ui first')
    if destination.exists() or source.resolve() in destination.parents:
        parser.error('Choose a new destination outside the build output')
    checksum = digest(source)
    shutil.copytree(source, destination)
    if digest(destination) != checksum:
        raise RuntimeError('Copied bundle does not match the built asset')
    print('resources:\n  asset:\n    pcma-tools:')
    print('      type: com.github.aux.d3add3v3r.prusaconnectmoonrakeradapter/pcma-tools')
    print('      enabled: true\n      asset:')
    print('        path: ' + json.dumps(str(destination)))
    print('        checksum: ' + checksum)


if __name__ == '__main__':
    main()
