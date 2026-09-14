#!/usr/bin/python3
"""Export a stopped build snapshot inside a separate disposable, offline VM.

All output is untrusted. The host must confirm exporter retirement, bind the
metadata to its assignment and independently validate the returned archive.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time
import traceback

DEV = Path('/dev/disk/by-id')
INPUT = DEV / 'virtio-deployer-input'
SNAPSHOT = DEV / 'virtio-deployer-snapshot'
OUTPUT = DEV / 'virtio-deployer-output'
BASE = Path('/var/lib/deployer-export')
HEADER_SIZE = 4096
MAX_ARCHIVE = 100 << 20


def require(condition, message):
    if not condition:
        raise ValueError(message)


def command(args, **kwargs):
    return subprocess.run(args, check=True, timeout=10, **kwargs)


def bounded_json(path):
    # Never follow a build-created symlink, even inside this disposable VM.
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as f:
        info = os.fstat(f.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_size <= 16384, 'invalid metadata file')
        return json.loads(f.read(16385))


def run():
    for _ in range(50):
        if INPUT.exists() and SNAPSHOT.exists() and OUTPUT.exists():
            break
        time.sleep(0.1)
    label = subprocess.run(['/usr/sbin/blkid', '-s', 'LABEL', '-o', 'value', str(INPUT)], capture_output=True, text=True, timeout=10)
    if label.stdout.strip() != 'DEPLOYER_EXPORT':
        return
    try:
        require(os.geteuid() == 0, 'root boot job required')
        for disk, readonly in [(INPUT, '1'), (SNAPSHOT, '1'), (OUTPUT, '0')]:
            require((Path('/sys/block') / disk.resolve().name / 'ro').read_text().strip() == readonly, 'incorrect disk access')
        sizes = {}
        for disk in [SNAPSHOT, OUTPUT]:
            sizes[disk] = int(command(['/usr/sbin/blockdev', '--getsize64', str(disk)], capture_output=True, text=True).stdout)
            require(64 << 20 <= sizes[disk] <= 512 << 20, 'disk size out of bounds')
        BASE.mkdir(mode=0o700)
        metadata = BASE / 'metadata'
        snapshot = BASE / 'snapshot'
        metadata.mkdir(mode=0o700)
        snapshot.mkdir(mode=0o700)
        command(['/usr/bin/mount', '-t', 'udf', '-o', 'ro,nodev,nosuid,noexec', str(INPUT), str(metadata)])
        packet = bounded_json(metadata / 'request.json')
        require(set(packet) == {'ExecutionID', 'SourceSHA256', 'SnapshotSHA256', 'NotAfter'}, 'invalid export request')
        for key in ['ExecutionID', 'SourceSHA256', 'SnapshotSHA256']:
            require(isinstance(packet[key], str) and re.fullmatch('[0-9a-f]{64}', packet[key]), 'invalid identity')
        require(1 < packet['NotAfter'] - time.time() <= 60, 'expired export request')
        digest = hashlib.sha256()
        with SNAPSHOT.open('rb') as source:
            remaining = sizes[SNAPSHOT]
            while remaining:
                chunk = source.read(min(1 << 20, remaining))
                require(chunk, 'truncated snapshot')
                digest.update(chunk)
                remaining -= len(chunk)
        require(digest.hexdigest() == packet['SnapshotSHA256'], 'snapshot identity mismatch')
        # Never replay the ext4 journal or execute files from the build disk.
        command(['/usr/bin/mount', '-t', 'ext4', '-o', 'ro,noload,nodev,nosuid,noexec', str(SNAPSHOT), str(snapshot)])
        result = bounded_json(snapshot / 'result.json')
        require(result.get('ExecutionID') == packet['ExecutionID'] and result.get('Outcome') == 'succeeded', 'build did not succeed')
        source = result.get('Source', {})
        require(source.get('SHA256') == packet['SourceSHA256'], 'source identity mismatch')
        directory = source.get('Directory', '')
        require(isinstance(directory, str) and re.fullmatch('source-[0-9a-f]{64}', directory), 'invalid source directory')
        work = snapshot / 'work'
        tree = work / directory
        require(stat.S_ISDIR(work.lstat().st_mode) and stat.S_ISDIR(tree.lstat().st_mode), 'invalid build tree')
        archive = BASE / 'release.zip'
        # Existing exporter bounds entries, bytes, paths and symlinks and validates
        # the final ZIP. Its process never executes any files from the snapshot.
        exported = subprocess.run(['/opt/deployer-build/node-artifact-export', str(tree), str(archive)], capture_output=True, timeout=max(1, packet['NotAfter'] - time.time()))
        require(exported.returncode == 0 and len(exported.stdout) <= 16384, 'archive export failed')
        manifest = json.loads(exported.stdout)
        size = archive.stat().st_size
        require(0 < size <= MAX_ARCHIVE and HEADER_SIZE + size <= sizes[OUTPUT], 'archive size out of bounds')
        header = {'Version': 1, 'ExecutionID': packet['ExecutionID'], 'SourceSHA256': packet['SourceSHA256'], 'SnapshotSHA256': packet['SnapshotSHA256'], 'ArchiveSHA256': manifest['SHA256'], 'ArchiveBytes': size}
        raw = json.dumps(header, sort_keys=True, separators=(',', ':')).encode()
        require(len(raw) < HEADER_SIZE, 'header too large')
        with OUTPUT.open('r+b', buffering=0) as target:
            # Publish the header last. An interrupted export remains invalid.
            require(target.read(HEADER_SIZE) == bytes(HEADER_SIZE), 'output already used')
            target.seek(HEADER_SIZE)
            with archive.open('rb') as data:
                while chunk := data.read(1 << 20):
                    require(target.write(chunk) == len(chunk), 'short archive write')
            os.fsync(target.fileno())
            target.seek(0)
            require(target.write(raw.ljust(HEADER_SIZE, b'\0')) == HEADER_SIZE, 'short header write')
            os.fsync(target.fileno())
        print('Guest export stage: succeeded; host validation and retirement required', flush=True)
    except Exception:
        traceback.print_exc()
        sys.stderr.flush()
        raise
    finally:
        command(['/usr/bin/systemctl', 'poweroff'], capture_output=True)


if __name__ == '__main__':
    run()
