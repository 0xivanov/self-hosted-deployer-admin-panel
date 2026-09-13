#!/usr/bin/python3
"""Synthetic disk-I/O fixture only. Never processes customer build inputs."""
from pathlib import Path
import os
import subprocess
import time
import errno

input_disk = Path('/dev/disk/by-id/virtio-deployer-input')
output_disk = Path('/dev/disk/by-id/virtio-deployer-output')
for _ in range(50):
    if input_disk.exists() and output_disk.exists():
        break
    time.sleep(0.1)
label = subprocess.run(['/usr/sbin/blkid', '-s', 'LABEL', '-o', 'value', str(input_disk)], capture_output=True, text=True)
if label.stdout.strip() != 'NODEBUILD_FIXTURE':
    serials = {p.parent.name: p.read_text().strip() for p in Path('/sys/block').glob('*/serial')}
    print('Synthetic disk fixture skipped:', repr(label.stdout), repr(label.stderr), serials, flush=True)
    raise SystemExit(0)
assert os.geteuid() == 0
assert (Path('/sys/block') / input_disk.resolve().name / 'ro').read_text().strip() == '1'
assert (Path('/sys/block') / output_disk.resolve().name / 'ro').read_text().strip() == '0'
target = Path('/run/node-disk-channel-probe')
target.mkdir(mode=0o700, exist_ok=True)
subprocess.run(['/usr/bin/mount', '-t', 'iso9660', '-o', 'ro,nodev,nosuid,noexec', str(input_disk), str(target)], check=True)
assert (target / 'fixture.txt').read_bytes() == b'NODE_BUILD_DISK_FIXTURE\n'
fd = None
try:
    fd = os.open(input_disk, os.O_WRONLY)
    os.pwrite(fd, b'X' * 512, 0)
    os.fsync(fd)
except OSError as error:
    assert error.errno in (errno.EROFS, errno.EPERM, errno.EACCES), error
else:
    raise RuntimeError('immutable input disk accepted a write')
finally:
    if fd is not None:
        os.close(fd)
with open(output_disk, 'r+b', buffering=0) as output:
    output.write(b'NODE_DISK_CHANNEL_OK\n')
    os.fsync(output.fileno())
print('Synthetic disk channels passed', flush=True)
subprocess.run(['/usr/bin/systemctl', 'poweroff'], check=True)
