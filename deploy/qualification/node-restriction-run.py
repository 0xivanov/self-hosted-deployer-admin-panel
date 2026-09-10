#!/usr/bin/env python3
"""Guest-only runner for trusted synthetic service restriction probes."""
import json
import os
from pathlib import Path
import subprocess
import uuid

assert os.getuid() != 0
from node_lab_profile import restriction_properties

properties = restriction_properties()
for mode in ['boundaries', 'cpu', 'memory', 'timeout']:
    unit = 'node-probe-' + mode + '-' + uuid.uuid4().hex[:12]
    command = ['sudo', '-n', 'systemd-run', '--wait', '--unit=' + unit]
    for key, value in properties.items():
        command.append('--property=' + key + '=' + value)
    command += ['/usr/bin/python3', '/opt/node-restriction-lab/probe.py', mode]
    try:
        run = subprocess.run(command, capture_output=True, text=True, timeout=25)
        show = subprocess.check_output(['sudo', '-n', 'systemctl', 'show', unit,
                                        '--property=Result,ActiveState'], text=True)
        journal = subprocess.check_output(['sudo', '-n', 'journalctl', '-u', unit, '--no-pager', '-o', 'cat'], text=True)
        expected = {'boundaries': 'success', 'cpu': 'success', 'memory': 'oom-kill', 'timeout': 'timeout'}[mode]
        assert 'Result=' + expected in show, (mode, run.returncode, show, journal)
        assert '"kernel_limits"' in journal, journal
        if mode == 'boundaries':
            assert '"boundaries": "passed"' in journal, journal
        if mode == 'cpu':
            assert '"cpu_throttled_periods"' in journal, journal
        if mode == 'timeout':
            child = int(next(line.split('=', 1)[1] for line in journal.splitlines() if line.startswith('child-pid=')))
            assert not Path(f'/proc/{child}').exists(), 'timed-out child process survived'
        print(json.dumps({'probe': mode, 'result': expected, 'properties': show.splitlines(),
                          'evidence': [line for line in journal.splitlines() if line.startswith('{') or line.startswith('child-pid=')]}), flush=True)
    finally:
        subprocess.run(['sudo', '-n', 'systemctl', 'stop', unit], capture_output=True, timeout=5)
        subprocess.run(['sudo', '-n', 'systemctl', 'reset-failed', unit], capture_output=True, timeout=5)
