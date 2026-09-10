#!/usr/bin/env python3
"""Stage the trusted runtime fixture in the already-running named disposable VM."""
import os
from pathlib import Path
import subprocess
import tempfile

repo=Path(__file__).resolve().parents[2]
vm='deployer-node-build-lab'
with tempfile.TemporaryDirectory(prefix='node-runtime-setup-') as directory:
    helper=Path(directory)/'node-runtime-unit'
    subprocess.run(['go','build','-o',str(helper),'./deploy/qualification/node-runtime-unit'],cwd=repo,env={**os.environ,'GOOS':'linux','GOARCH':'arm64','CGO_ENABLED':'0'},check=True)
    installer=Path(directory)/'node-runtime-install'
    subprocess.run(['go','build','-o',str(installer),'./deploy/qualification/node-runtime-install'],cwd=repo,env={**os.environ,'GOOS':'linux','GOARCH':'arm64','CGO_ENABLED':'0'},check=True)
    control=Path(directory)/'node-runtime-control'
    subprocess.run(['go','build','-o',str(control),'./deploy/qualification/node-runtime-control'],cwd=repo,env={**os.environ,'GOOS':'linux','GOARCH':'arm64','CGO_ENABLED':'0'},check=True)
    files=[helper,installer,control,repo/'deploy/qualification/node-runtime-service.py',repo/'deploy/qualification/node-runtime-probe.mjs']
    subprocess.run(['limactl','copy',*map(str,files),vm+':/tmp/'],check=True)
    subprocess.run(['limactl','shell',vm,'python3','/tmp/node-runtime-service.py'],check=True,timeout=90)
