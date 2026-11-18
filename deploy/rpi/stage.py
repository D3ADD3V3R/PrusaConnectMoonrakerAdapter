"""Copy repository sources into a separate Raspberry Pi build directory."""

import argparse
from pathlib import Path
import shutil


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('destination', type=Path)
    args = parser.parse_args()
    repository = Path(__file__).resolve().parents[2]
    deployment = Path(__file__).resolve().parent
    destination = args.destination.expanduser().resolve()
    if destination == repository or repository in destination.parents or destination in repository.parents:
        parser.error('Choose a build directory separate from the repository.')
    if (destination / 'prusa-connect').exists() or (destination / 'config').exists():
        parser.error('The destination looks like an installed PC system.')
    destination.mkdir(parents=True, exist_ok=True)
    ignored = shutil.ignore_patterns('__pycache__', '*.pyc', 'node_modules', '.git')
    for name in ('adapter', 'klipper', 'tests'):
        shutil.copytree(repository / name, destination / name, dirs_exist_ok=True, ignore=ignored)
    for name in ('scripts', 'tests'):
        shutil.copytree(deployment / name, destination / name, dirs_exist_ok=True, ignore=ignored)
    for name in ('Makefile', 'versions.env', 'build.yaml', 'upstream.patch'):
        shutil.copyfile(deployment / name, destination / name)
    for name in ('README.md',):
        shutil.copyfile(deployment / name, destination / name)
    for name in ('Readme.md', 'LICENCE.md', 'NOTICE.md'):
        shutil.copyfile(repository / name, destination / ('PROJECT-' + name))
    print(f'Sources staged in {destination}')
    print(f'cd {destination}\nmake\nmake install')


if __name__ == '__main__':
    main()
