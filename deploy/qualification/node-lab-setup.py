#!/usr/bin/env python3
"""Mac helper: stage only the checked-in trusted fixture in the named running lab.
Does not create/start VMs. Installs no host Node packages. No customer inputs.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import zipfile

parser = argparse.ArgumentParser()
parser.add_argument('--dependency', action='store_true')
args = parser.parse_args()
repo = Path(__file__).resolve().parents[2]
vm = 'deployer-node-build-lab'
with tempfile.TemporaryDirectory(prefix='node-rehearsal-setup-') as directory:
    stage = Path(directory)
    archive = stage / 'node-lab-source.zip'
    fixture = repo / 'deploy/qualification' / ('node-dependency-app' if args.dependency else 'node-app')
    with zipfile.ZipFile(archive, 'w') as out:
        for source in sorted(fixture.rglob('*')):
            if source.is_file():
                info = zipfile.ZipInfo(source.relative_to(fixture).as_posix())
                info.external_attr = 0o100644 << 16
                out.writestr(info, source.read_bytes())
    helper = stage / 'node-lab-preflight'
    subprocess.run(['go', 'build', '-o', str(helper), './deploy/qualification/node-preflight'],
                   cwd=repo, env={**os.environ, 'GOOS': 'linux', 'GOARCH': 'arm64', 'CGO_ENABLED': '0'}, check=True)
    files = [archive, helper, *[repo / 'deploy/qualification' / name for name in
                              ['node-rehearsal.py', 'node-positive-run.py', 'node_lab_profile.py',
                               'node-restriction-run.py', 'node-restriction-probe.py']]]
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    packages = stage / 'packages'
    if args.dependency:
        packages.mkdir(mode=0o700)
        bundle = json.loads(subprocess.check_output(['go', 'run', './deploy/qualification/node-prefetch', str(archive), digest, str(packages)], cwd=repo, text=True))
        packages = packages / bundle['Directory']
        files += list(packages.iterdir())
    subprocess.run(['limactl', 'copy', *map(str, files), vm + ':/tmp/'], check=True)
    script = '''set -eu
sudo install -d -m 755 /opt/node-positive-lab /opt/node-restriction-lab
sudo install -m 644 /tmp/node-lab-source.zip /opt/node-positive-lab/source.zip
sudo install -m 755 /tmp/node-lab-preflight /opt/node-positive-lab/node-preflight
sudo install -m 644 /tmp/node-rehearsal.py /opt/node-positive-lab/node-rehearsal.py
sudo install -m 644 /tmp/node-restriction-probe.py /opt/node-restriction-lab/probe.py
sudo curl --fail --silent --show-error -o /opt/node-positive-lab/node.tar.xz https://nodejs.org/dist/v24.20.0/node-v24.20.0-linux-arm64.tar.xz
cd /opt/node-positive-lab
echo '5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7  node.tar.xz' | sha256sum -c -
sudo tar --no-same-owner -xf node.tar.xz
'''
    subprocess.run(['limactl', 'shell', vm, 'bash', '-c', script], check=True)
    if args.dependency:
        subprocess.run(['limactl', 'shell', vm, 'sudo', 'install', '-d', '-m', '755', '/opt/node-positive-lab/packages'], check=True)
        for package in packages.iterdir():
            subprocess.run(['limactl', 'shell', vm, 'sudo', 'install', '-m', '644', '/tmp/' + package.name,
                            '/opt/node-positive-lab/packages/' + package.name], check=True)
    print('Fixture SHA-256: ' + digest, flush=True)
    subprocess.run(['limactl', 'shell', vm, 'python3', '/tmp/node-positive-run.py', digest, *(['--dependency'] if args.dependency else [])], check=True)
