#!/usr/bin/env python3
"""Trusted negative probes for a transient service in the disposable Node VM."""
import errno
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import time

mode = sys.argv[1]
assert os.getuid() != 0
cgroup = Path('/sys/fs/cgroup') / Path('/proc/self/cgroup').read_text().strip().split('::', 1)[1].lstrip('/')
limits = {key: (cgroup / key).read_text().strip() for key in ['memory.max', 'memory.swap.max', 'pids.max', 'cpu.max']}
assert limits['memory.max'] == '134217728' and limits['memory.swap.max'] == '0' and limits['pids.max'] == '32', limits
quota, period = map(int, limits['cpu.max'].split())
assert quota == period, limits
print(json.dumps({'kernel_limits': limits}), flush=True)
if mode == 'boundaries':
    assert 'NoNewPrivs:\t1' in Path('/proc/self/status').read_text()
    for address in ['169.254.169.254', '10.8.0.1', '1.1.1.1']:
        try:
            with socket.create_connection((address, 80), timeout=1):
                raise AssertionError('outbound connection succeeded: ' + address)
        except OSError:
            pass
    try:
        Path('/etc/node-probe-write').write_text('must fail')
        raise AssertionError('system directory writable')
    except OSError:
        pass
    # Verify no-new-privileges blocks the guest account's usual sudo elevation.
    elevated = subprocess.run(['sudo', '-n', 'id', '-u'], capture_output=True, timeout=3)
    assert elevated.returncode != 0, 'sudo elevation succeeded'
    Path('/work/check').write_text('private writable job storage')
    children = []
    try:
        try:
            for _ in range(64):
                children.append(subprocess.Popen(['/usr/bin/sleep', '30']))
        except OSError as error:
            assert error.errno == errno.EAGAIN, error
        else:
            raise AssertionError('process limit was not enforced')
        assert len(children) < 32
    finally:
        for child in children:
            child.terminate()
        for child in children:
            child.wait(timeout=3)
    signal.signal(signal.SIGXFSZ, signal.SIG_IGN)
    try:
        with open('/work/oversized', 'wb') as output:
            output.write(b'x' * (5 * 1024 * 1024))
        raise AssertionError('file size limit was not enforced')
    except OSError as error:
        assert error.errno == errno.EFBIG, error
    # Fill the job tmpfs across many files, each below the per-file cap.
    try:
        for index in range(100):
            Path(f'/work/fill-{index}').write_bytes(b'x' * (1024 * 1024))
        raise AssertionError('aggregate job disk limit was not enforced')
    except OSError as error:
        assert error.errno == errno.ENOSPC, error
    for file in Path('/work').iterdir():
        file.unlink()
    for temporary in ['/tmp', '/var/tmp']:
        try:
            for index in range(32):
                Path(f'{temporary}/fill-{index}').write_bytes(b'x' * (1024 * 1024))
            raise AssertionError('temporary disk limit was not enforced')
        except OSError as error:
            assert error.errno == errno.ENOSPC, error
        for file in Path(temporary).glob('fill-*'):
            file.unlink()
    print(json.dumps({'boundaries': 'passed', 'children_before_limit': len(children),
                      'network_blocked': True, 'sudo_blocked': True, 'file_limit': True, 'job_disk_limit': True, 'temporary_disk_limits': True}), flush=True)
elif mode == 'cpu':
    children = [subprocess.Popen(['/usr/bin/python3', '-c', 'while True: pass']) for _ in range(2)]
    try:
        time.sleep(2)
        cpu = dict(line.split() for line in (cgroup / 'cpu.stat').read_text().splitlines())
        assert int(cpu['nr_throttled']) > 0, cpu
        print(json.dumps({'cpu_throttled_periods': int(cpu['nr_throttled'])}), flush=True)
    finally:
        for child in children:
            child.terminate()
        for child in children:
            child.wait(timeout=3)
elif mode == 'memory':
    held = []
    while True:
        held.append(bytearray(8 * 1024 * 1024))
elif mode == 'timeout':
    child = subprocess.Popen(['/usr/bin/sleep', '300'])
    print('child-pid=' + str(child.pid), flush=True)
    while True:
        time.sleep(1)
else:
    raise RuntimeError('unknown probe')
