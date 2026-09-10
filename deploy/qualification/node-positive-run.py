#!/usr/bin/env python3
"""Run the trusted Node fixture under the shared restriction profile in the guest."""
import json
import os
import subprocess
import sys
import re
import uuid
from node_lab_profile import restriction_properties

assert os.getuid() != 0
assert len(sys.argv) == 2 and re.fullmatch(r"[0-9a-f]{64}", sys.argv[1]), "pass the fixture ZIP SHA-256"
unit = 'node-positive-' + uuid.uuid4().hex[:12]
properties = restriction_properties()
command = ['sudo', '-n', 'systemd-run', '--wait', '--unit=' + unit]
for key, value in properties.items():
    command.append('--property=' + key + '=' + value)
base = '/opt/node-positive-lab'
command += ['/usr/bin/python3', base + '/node-rehearsal.py', base + '/source.zip',
            sys.argv[1],
            base + '/node-preflight', base + '/node-v24.20.0-linux-arm64/bin']
try:
    result = subprocess.run(command, capture_output=True, text=True, timeout=25)
    journal = subprocess.check_output(['sudo', '-n', 'journalctl', '-u', unit, '--no-pager', '-o', 'cat'], text=True)
    assert result.returncode == 0, (result.stderr, journal)
    report = next(json.loads(line) for line in journal.splitlines() if line.startswith('{"result":'))
    assert report['result'] == 'passed', report
    print(json.dumps({'restricted_node': report, 'profile': properties}), flush=True)
finally:
    subprocess.run(['sudo', '-n', 'systemctl', 'stop', unit], capture_output=True, timeout=5)
    subprocess.run(['sudo', '-n', 'systemctl', 'reset-failed', unit], capture_output=True, timeout=5)
