#!/usr/bin/python3
"""Trusted image boot job. Guest output is never VM retirement evidence."""
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import stat
import subprocess
import time
import traceback

INPUT = Path('/dev/disk/by-id/virtio-deployer-input')
OUTPUT = Path('/dev/disk/by-id/virtio-deployer-output')
BASE = Path('/var/lib/deployer-build')
MOUNT = Path('/mnt/deployer-build-output')
UID = 60000


def command(args, **kwargs):
    return subprocess.run(args, check=True, timeout=10, **kwargs)


def private_copy(source, destination):
    """Copy bounded input data only; do not follow ISO links or run its scripts."""
    destination.mkdir(mode=0o700)
    os.chown(destination, UID, UID)
    total = count = 0
    for directory, dirs, files in os.walk(source, followlinks=False):
        relative = Path(directory).relative_to(source)
        if len(relative.parts) > 64:
            raise ValueError('input tree too deep')
        for name in dirs + files:
            count += 1
            if count > 20000:
                raise ValueError('too many input entries')
            path = Path(directory) / name
            info = path.lstat()
            target = destination / relative / name
            if stat.S_ISDIR(info.st_mode):
                target.mkdir(mode=0o700)
            elif stat.S_ISREG(info.st_mode):
                total += info.st_size
                if total > 128 << 20:
                    raise ValueError('input exceeds limit')
                with path.open('rb') as src, target.open('xb') as dst:
                    shutil.copyfileobj(src, dst, 65536)
                target.chmod(0o600)
            else:
                raise ValueError('unsupported input entry')
            os.chown(target, UID, UID)


def run():
    assert os.geteuid() == 0
    for _ in range(50):
        if INPUT.exists() and OUTPUT.exists():
            break
        time.sleep(0.1)
    label = subprocess.run(['/usr/sbin/blkid', '-s', 'LABEL', '-o', 'value', str(INPUT)], capture_output=True, text=True)
    if label.stdout.strip() != 'DEPLOYER_BUILD':
        print('Guest build skipped: no build input label', flush=True)
        return
    # From here the dedicated build VM always powers off, including on failure.
    try:
        account = pwd.getpwuid(UID)
        assert account.pw_gid == UID and os.getgrouplist(account.pw_name, UID) == [UID]
        for disk, readonly in [(INPUT, '1'), (OUTPUT, '0')]:
            assert (Path('/sys/block') / disk.resolve().name / 'ro').read_text().strip() == readonly
        size = int(command(['/usr/sbin/blockdev', '--getsize64', str(OUTPUT)], capture_output=True, text=True).stdout)
        assert 64 << 20 <= size <= 512 << 20
        # Never reformat an output disk that already has a filesystem signature.
        signature = subprocess.run(['/usr/sbin/blkid', '-p', str(OUTPUT)], capture_output=True)
        assert signature.returncode == 2
        BASE.mkdir(mode=0o755)  # A reused boot image fails instead of rerunning.
        iso = BASE / 'iso'
        iso.mkdir(mode=0o700)
        command(['/usr/bin/mount', '-t', 'udf', '-o', 'ro,nodev,nosuid,noexec', str(INPUT), str(iso)])
        metadata = iso / 'request.json'
        assert metadata.stat().st_size <= 16384
        packet = json.loads(metadata.read_text())
        assert set(packet) == {'ExecutionID', 'Plan', 'ToolchainSHA256', 'Bundle', 'NotAfter'}
        assert re.fullmatch('[0-9a-f]{64}', packet['ExecutionID'])
        remaining = packet['NotAfter'] - time.time()
        assert 1 < remaining <= 60
        private_copy(iso, BASE / 'input')
        command(['/usr/sbin/mkfs.ext4', '-q', '-F', str(OUTPUT)], capture_output=True)
        MOUNT.mkdir(mode=0o755, exist_ok=True)
        command(['/usr/bin/mount', '-o', 'nodev,nosuid', str(OUTPUT), str(MOUNT)])
        work = MOUNT / 'work'
        work.mkdir(mode=0o700)
        os.chown(work, UID, UID)
        request = {k: packet[k] for k in ['Plan', 'ToolchainSHA256', 'Bundle', 'NotAfter']}
        request.update(SourcePath=str(BASE / 'input/source.zip'), DependenciesPath=str(BASE / 'input'), WorkDirectory=str(work))
        request_file = BASE / 'request.json'
        request_file.write_text(json.dumps(request))
        request_file.chmod(0o600)
        os.chown(request_file, UID, UID)
        unit = 'deployer-build-' + packet['ExecutionID']
        properties = {
            'User': str(UID), 'Group': str(UID), 'NoNewPrivileges': 'yes',
            'PrivateNetwork': 'yes', 'PrivateDevices': 'yes', 'ProtectSystem': 'strict',
            'ProtectHome': 'yes', 'ProtectControlGroups': 'yes', 'ProtectKernelTunables': 'yes',
            'ProtectKernelModules': 'yes', 'RestrictSUIDSGID': 'yes', 'RestrictNamespaces': 'yes',
            'CapabilityBoundingSet': '', 'MemoryMax': '512M', 'MemorySwapMax': '0',
            'TasksMax': '128', 'CPUQuota': '100%', 'KillMode': 'control-group', 'TimeoutStopSec': '2',
            'RuntimeMaxSec': str(max(1, int(packet['NotAfter'] - time.time()))),
            'ReadOnlyPaths': str(BASE / 'input'), 'ReadWritePaths': str(work),
            'TemporaryFileSystem': '/tmp:rw,size=64M,mode=1777 /var/tmp:rw,size=16M,mode=1777',
            'WorkingDirectory': str(work),
        }
        args = ['/usr/bin/systemd-run', '--quiet', '--wait', '--pipe', '--unit=' + unit]
        args += ['--property=' + k + '=' + v for k, v in properties.items()]
        args += ['/opt/deployer-build/node-build-guest', str(request_file)]
        result = None
        try:
            # Guest helper caps npm output; the VM itself has no host data channel
            # except a separately capped console and its dedicated output disk.
            result = subprocess.run(args, capture_output=True, timeout=max(1, packet['NotAfter'] - time.time()) + 5)
        finally:
            stopped = subprocess.run(['/usr/bin/systemctl', 'stop', unit], capture_output=True, timeout=10)
            # systemd can garbage-collect a completed transient unit before stop.
            if stopped.returncode not in (0, 5):
                raise RuntimeError('build service cleanup failed')
        with (MOUNT / 'build.log').open('xb') as log:
            os.fchmod(log.fileno(), 0o600)
            log.write(result.stderr[:1 << 20])
        record = {'ExecutionID': packet['ExecutionID'], 'Outcome': 'failed'}
        if result.returncode == 0 and len(result.stdout) <= 16384:
            source = json.loads(result.stdout)
            assert re.fullmatch('source-[0-9a-f]{64}', source['Directory'])
            assert source['SHA256'] == packet['Plan']['source_sha256']
            record.update(Outcome='succeeded', Source=source)
        with (MOUNT / 'result.json').open('x') as output:
            os.chmod(output.fileno(), 0o600)
            json.dump(record, output)
            output.flush()
            os.fsync(output.fileno())
        os.sync()
        print('Guest build stage: ' + record['Outcome'] + '; host must confirm VM stop before export', flush=True)
    except Exception:
        traceback.print_exc()
        import sys
        sys.stderr.flush()
        raise
    finally:
        command(['/usr/bin/systemctl', 'poweroff'], capture_output=True)


if __name__ == '__main__':
    run()
