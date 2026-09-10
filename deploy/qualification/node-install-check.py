#!/usr/bin/env python3
"""Run trusted root installer, control and usage tests in the already-running disposable ARM64 VM."""
import os
from pathlib import Path
import subprocess
import tempfile

repo = Path(__file__).resolve().parents[2]
vm = 'deployer-node-build-lab'
with tempfile.TemporaryDirectory(prefix='node-install-check-') as directory:
    binary = Path(directory) / 'nodelaunch.test'
    subprocess.run(['go', 'test', '-c', '-tags=integration', '-o', str(binary), './internal/nodelaunch'],
                   cwd=repo, env={**os.environ, 'GOOS': 'linux', 'GOARCH': 'arm64', 'CGO_ENABLED': '0'}, check=True)
    subprocess.run(['limactl', 'copy', str(binary), vm + ':/tmp/nodelaunch-install.test'], check=True)
    subprocess.run(['limactl', 'shell', vm, 'sudo', '-n', '/tmp/nodelaunch-install.test',
                    '-test.run=^(TestInstallRelease|TestControlGate|TestRuntimeUsage)', '-test.v', '-test.timeout=60s'], check=True, timeout=75)
