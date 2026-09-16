#!/usr/bin/env python3
"""Capture the restricted Pi backup stream before the offsite recovery backup."""
import os, subprocess, tempfile
from pathlib import Path
root=Path('/var/lib/launchstead-fleet-backups');root.mkdir(mode=0o700,exist_ok=True)
fd,tmp=tempfile.mkstemp(dir=root,prefix='.capture-')
try:
 with os.fdopen(fd,'wb') as out:
  subprocess.run(['ssh','-T','-o','BatchMode=yes','-o','ConnectTimeout=10','-o','StrictHostKeyChecking=yes','-o','UserKnownHostsFile=/etc/launchstead-registry/builder-known-hosts','-i','/etc/launchstead-registry/builder-backup-key','deployer@10.8.0.3'],stdout=out,check=True,timeout=120)
  out.flush();os.fsync(out.fileno())
 subprocess.run(['tar','-tzf',tmp],stdout=subprocess.DEVNULL,check=True,timeout=30)
 os.replace(tmp,root/'builder-recovery.tar.gz')
 fd=os.open(root,os.O_DIRECTORY)
 try:os.fsync(fd)
 finally:os.close(fd)
finally:
 if os.path.exists(tmp):os.unlink(tmp)
