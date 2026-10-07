#!/usr/bin/env python3
"""Check public source files and repository-contained documentation links."""
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parent
TEXT_SUFFIXES = {'.go', '.sql', '.md', '.yaml', '.yml', '.py', '.json', '.mod', '.sum', '.html', '.css'}


def source_files():
    result = subprocess.run(
        ['git', '-C', str(ROOT), 'ls-files', '-z', '--cached', '--others', '--exclude-standard'],
        check=True, stdout=subprocess.PIPE,
    )
    return sorted(set(p.decode('utf-8') for p in result.stdout.split(b'\0') if p))


def check():
    issues = []
    files = source_files()
    for relative in files:
        path = ROOT / relative
        if not path.exists():
            continue  # Files removed from the working tree may still be in the index.
        if path.is_symlink() or not path.resolve().is_relative_to(ROOT):
            issues.append(f'Unsupported filesystem link: {relative}')
            continue
        if not path.is_file():
            continue
        if re.search(r'(?i)(?:review|audit|审查|审计).*(?:\.md|\.jsonl)$', relative):
            issues.append(f'Historical review artifact: {relative}')
        if re.search(r'(?i)(?:^|/)(?:.*review.*|.*capture.*|.*export.*)\.(?:jsonl|har|pcap|dump)$', relative):
            issues.append(f'Private diagnostic input: {relative}')
        if path.suffix not in TEXT_SUFFIXES and path.name not in {'Makefile', 'Dockerfile', 'LICENSE', '.gitignore', '.dockerignore', '.gitattributes'}:
            issues.append(f'Unclassified publication file: {relative}')
            continue
        body = path.read_text(encoding='utf-8')
        # Fixed identity literals must be visibly synthetic; generated tokens are
        # checked by their behavior in tests rather than recorded from a player.
        if path.suffix in {'.go', '.json', '.md', '.html', '.yaml'}:
            for identifier in re.findall(r'\b[0-9a-fA-F]{32}\b', body):
                if identifier.lower() != 'a' * 31 + 'b':
                    issues.append(f'Unclassified fixed identity/token: {relative}')
        for pattern, label in [
            (r'(?<![A-Za-z])[A-Za-z]:[\\/](?!/)', 'Absolute Windows path'),
            (r'(?<![\w:])/(?:home|Users|workspace|mnt|abs|tmp|etc|opt)/', 'Absolute host path'),
            (r'(?i)\b(?:heaven' r'burnsred|Karerin' r'-dev)\b', 'Private workspace or old repository marker'),
            (r'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----', 'Private key'),
        ]:
            if label == 'Absolute host path' and path.name in {'.gitignore', '.dockerignore'}:
                continue  # Leading slashes anchor ignore rules to this repository.
            if re.search(pattern, body):
                issues.append(f'{label}: {relative}')
        if path.suffix in {'.go', '.sql', '.py'}:
            for line in body.splitlines():
                if re.match(r'\s*(?://|/\*|\*|--|#)', line) and re.search(r'(?i)\b(?=[0-9a-f]*[a-f])[0-9a-f]{7,40}\b', line):
                    # Hex algorithm constants are written with a 0x prefix.
                    issues.append(f'Commit/hash annotation in source comment: {relative}')
                    break
        if path.suffix == '.md':
            prose = re.sub(r'(?ms)^```.*?^```[^\n]*', '', body)
            for target in re.findall(r'\]\(([^)]+)\)', prose):
                target = target.split('#', 1)[0].strip('<>')
                if not target or re.match(r'^[a-z][a-z0-9+.-]*://', target, re.I):
                    continue
                resolved = (path.parent / target).resolve()
                if not resolved.is_relative_to(ROOT):
                    issues.append(f'External file link: {relative} -> {target}')
                elif not resolved.exists():
                    issues.append(f'Missing file link: {relative} -> {target}')
    seed = json.loads((ROOT / 'internal/model/newplayer_seed.json').read_text(encoding='utf-8'))
    if seed['tables'].get('user_server_gift'):
        issues.append('Public starter template contains campaign inbox gifts')
    personal = re.compile(r'(?i)(?:userName|comment|country|language|(?:^|_)userId|x_uid|token|password|email)$')
    timestamp = re.compile(r'(?i)(?:At|Date|Datetime|Time)$')

    def scan(value, key=''):
        if isinstance(value, dict):
            for child_key, child in value.items():
                scan(child, child_key)
        elif isinstance(value, list):
            for child in value:
                scan(child, key)
        elif (personal.search(key) or timestamp.search(key)) and value not in ('', 0, None):
            issues.append(f'Non-neutral starter field: {key}')
        elif isinstance(value, int) and not isinstance(value, bool) and value >= 1_000_000_000_000:
            issues.append(f'Instance identifier in starter template: {key}')
    scan(seed['tables'])
    examples = [ROOT / 'config.example.yaml', ROOT / 'testdata/golden/app_start.json']
    for example in examples:
        body = example.read_text(encoding='utf-8')
        if re.search(r'(?i)[\"\'](?:x_uid|userId|email|password|migration_code|migration_token)[\"\']\s*:\s*[\"\'][^<>\"\']+', body):
            issues.append(f'Player/credential data in public example: {example.relative_to(ROOT)}')
    if issues:
        raise SystemExit('\n'.join(sorted(set(issues))))
    print(f'Source hygiene passed ({len(files)} repository entries; container paths and API routes retained).', flush=True)
    print('Pattern checks complement manual review; they cannot prove arbitrary data is anonymous.')


if __name__ == '__main__':
    check()
