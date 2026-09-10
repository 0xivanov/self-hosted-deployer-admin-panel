#!/usr/bin/env python3
"""Run the trusted Node fixture under the shared restriction profile in the guest."""
import json
import os
import subprocess
import argparse
import re
import uuid
import time
from pathlib import Path
from node_lab_profile import restriction_properties

assert os.getuid() != 0
parser = argparse.ArgumentParser()
parser.add_argument('digest')
parser.add_argument('--dependency', action='store_true')
parser.add_argument('--execution-id')
parser.add_argument('--not-after', type=int)
args = parser.parse_args()
assert re.fullmatch(r"[0-9a-f]{64}", args.digest), 'pass the fixture ZIP SHA-256'
if bool(args.execution_id) != bool(args.not_after):
    raise ValueError('execution ID and deadline are required together')
if args.execution_id:
    assert re.fullmatch(r'[0-9a-f]{64}', args.execution_id)
    assert 15 <= args.not_after - time.time() <= 60, 'invalid or nearly expired dispatch'
    attempts = Path('/var/tmp') / ('node-worker-attempts-' + str(os.getuid()))
    attempts.mkdir(mode=0o700, exist_ok=True)
    info = attempts.lstat()
    assert attempts.is_dir() and not attempts.is_symlink() and info.st_uid == os.getuid() and info.st_mode & 0o077 == 0
    # Retained even after failure. This lab never retries an execution ID.
    fd = os.open(attempts / args.execution_id, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as record:
        json.dump({'execution_id': args.execution_id, 'not_after': args.not_after}, record)
        record.flush()
        os.fsync(record.fileno())
    directory = os.open(attempts, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)
unit = 'node-positive-' + (args.execution_id or uuid.uuid4().hex[:12])
properties = restriction_properties()
command = ['sudo', '-n', 'systemd-run', '--wait', '--unit=' + unit]
for key, value in properties.items():
    command.append('--property=' + key + '=' + value)
base = '/opt/node-positive-lab'
command += ['/usr/bin/python3', base + '/node-rehearsal.py', base + '/source.zip',
            args.digest,
            base + '/node-preflight', base + '/node-v24.20.0-linux-arm64/bin']
if args.dependency:
    command += [base + '/packages/bundle.json']
try:
    result = subprocess.run(command, capture_output=True, text=True, timeout=min(25, args.not_after - time.time()) if args.not_after else 25)
    journal = subprocess.check_output(['sudo', '-n', 'journalctl', '-u', unit, '--no-pager', '-o', 'cat'], text=True)
    assert result.returncode == 0, (result.stderr, journal)
    report = next(json.loads(line) for line in journal.splitlines() if line.startswith('{"result":'))
    assert report['result'] == 'passed', report
finally:
    subprocess.run(['sudo', '-n', 'systemctl', 'stop', unit], capture_output=True, timeout=5)
    state = subprocess.check_output(['sudo', '-n', 'systemctl', 'show', unit, '--property=ActiveState', '--property=MainPID'], text=True, timeout=5)
    fields = dict(line.split('=', 1) for line in state.splitlines())
    assert fields.get('ActiveState') in ('inactive', 'failed') and fields.get('MainPID', '0') == '0', state
    subprocess.run(['sudo', '-n', 'systemctl', 'reset-failed', unit], capture_output=True, timeout=5)

print(json.dumps({'restricted_node': report, 'profile': properties, 'execution_id': args.execution_id}), flush=True)
