#!/usr/bin/env python3
"""Validate and package the server using Python's standard library and Go."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import zipfile

from check_source import ROOT, check

PLATFORMS = ('linux-amd64', 'linux-arm64', 'windows-amd64')


def run(*args, env=None):
    subprocess.run(args, cwd=ROOT, env=env, check=True)


def output(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True, encoding='utf-8')


def license_files(stage):
    """Ship upstream notices, including Go's standard library license."""
    directory = stage / 'licenses'
    directory.mkdir()
    decoder = json.JSONDecoder()
    required = json.loads(output('go', 'mod', 'edit', '-json'))['Require']
    remaining = output('go', 'list', '-m', '-json', *(entry['Path'] for entry in required)).strip()
    inventory = []
    while remaining:
        module, end = decoder.raw_decode(remaining)
        remaining = remaining[end:].lstrip()
        if module.get('Main'):
            continue
        if module.get('Replace'):
            raise SystemExit('Review module replacements before packaging.')
        source = Path(module['Dir'])
        destination = directory / module['Path'].replace('/', '_')
        destination.mkdir()
        notices = [p for p in source.iterdir() if p.is_file() and
                   (p.name.upper().startswith(('LICENSE', 'COPYING', 'NOTICE')) or p.name.upper() == 'PATENTS')]
        if not any(p.name.upper().startswith(('LICENSE', 'COPYING')) for p in notices):
            raise SystemExit(f'Missing upstream license: {module["Path"]}')
        for notice in notices:
            shutil.copyfile(notice, destination / notice.name)
        inventory.append({'module': module['Path'], 'version': module['Version'],
                          'notices': [f'{destination.name}/{p.name}' for p in notices]})
    go_root = Path(output('go', 'env', 'GOROOT').strip())
    shutil.copyfile(go_root / 'LICENSE', directory / 'Go-LICENSE')
    inventory.append({'module': 'Go standard library', 'version': output('go', 'version').strip(),
                      'notices': ['Go-LICENSE']})
    (directory / 'dependencies.json').write_text(json.dumps(inventory, indent=2) + '\n', encoding='utf-8')


def scan_package(stage):
    forbidden = [str(ROOT), str(ROOT).replace('\\', '/'),
                 output('go', 'env', 'GOMODCACHE').strip(), output('go', 'env', 'GOROOT').strip()]
    for path in stage.rglob('*'):
        if not path.is_file():
            continue
        content = path.read_bytes()
        for marker in forbidden:
            for encoding in ('utf-8', 'utf-16le'):
                if marker.encode(encoding) in content:
                    raise SystemExit(f'Build-machine path in package: {path.name}')
        if re.search(rb'[A-Za-z]:[\\/](?:work|Users)[\\/]|/(?:home|Users)/', content, re.I):
            raise SystemExit(f'Host path in package: {path.name}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', default='dev')
    parser.add_argument('--platforms', nargs='+', choices=PLATFORMS, default=list(PLATFORMS))
    parser.add_argument('--skip-tests', action='store_true', help='Local packaging iteration only')
    parser.add_argument('--require-database', action='store_true')
    parser.add_argument('--race', action='store_true', help='Run tests with the Go race detector')
    args = parser.parse_args()
    if not re.fullmatch(r'(?:dev|\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?)', args.version):
        parser.error('Version must be dev or X.Y.Z with an optional prerelease suffix.')
    if args.require_database and (args.skip_tests or not os.environ.get('LILYPAD_TEST_DSN')):
        parser.error('Release validation needs LILYPAD_TEST_DSN and enabled tests.')
    check()
    run('go', 'mod', 'download')
    run('go', 'mod', 'verify')
    go_files = [str(p.relative_to(ROOT)) for parent in ('cmd', 'internal') for p in (ROOT / parent).rglob('*.go')]
    unformatted = output('gofmt', '-l', *go_files).strip()
    if unformatted:
        raise SystemExit('Run gofmt on:\n' + unformatted)
    run('go', 'vet', './...')
    if not args.skip_tests:
        run('go', 'test', *(['-race'] if args.race else []), './...')
        if not os.environ.get('LILYPAD_TEST_DSN'):
            print('Database integration tests skipped: LILYPAD_TEST_DSN is not set.', flush=True)
    destination = ROOT / 'dist' / 'releases' / args.version
    destination.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='lilypad-build-') as temporary:
        common = Path(temporary) / 'common'
        common.mkdir()
        for name in ('README.md', 'LICENSE', 'config.example.yaml'):
            shutil.copyfile(ROOT / name, common / name)
        (common / 'lilypad.version').write_text(args.version + '\n', encoding='utf-8')
        license_files(common)
        staged_archives = []
        for platform in dict.fromkeys(args.platforms):
            go_os, go_arch = platform.split('-')
            stage = Path(temporary) / platform
            shutil.copytree(common, stage)
            binary = stage / ('lilypad.exe' if go_os == 'windows' else 'lilypad')
            env = dict(os.environ, GOOS=go_os, GOARCH=go_arch, CGO_ENABLED='0')
            run('go', 'build', '-trimpath', '-buildvcs=false', '-ldflags=-s -w', '-o', str(binary), './cmd/lilypad', env=env)
            scan_package(stage)
            archive = Path(temporary) / f'lilypad-v{args.version}-{platform}.zip'
            with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as package:
                for path in sorted(stage.rglob('*')):
                    if path.is_file():
                        info = zipfile.ZipInfo(path.relative_to(stage).as_posix(), (1980, 1, 1, 0, 0, 0))
                        info.create_system = 3
                        mode = 0o100755 if path == binary else 0o100644
                        info.external_attr = mode << 16
                        info.compress_type = zipfile.ZIP_DEFLATED
                        package.writestr(info, path.read_bytes())
            checksum = archive.with_suffix('.zip.sha256')
            checksum.write_text(f'{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n', encoding='ascii')
            staged_archives.extend((archive, checksum))
        # Publish local artifacts only after every requested target passes validation.
        for artifact in staged_archives:
            shutil.copyfile(artifact, destination / artifact.name)
    print(f'Packages ready: dist/releases/{args.version}/')


if __name__ == '__main__':
    main()
