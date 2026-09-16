#!/usr/bin/env python3
"""Install private registry client settings; preserves unrelated Docker auth."""
import base64,json,os,shutil
from pathlib import Path
host='10.8.0.1:5000'
settings=json.loads(Path('/tmp/launchstead-registry-auth.json').read_text())
ca=Path('/etc/rancher/k3s/launchstead-registry-ca.crt');ca.parent.mkdir(parents=True,exist_ok=True)
shutil.copyfile('/tmp/registry.crt',ca);ca.chmod(0o644)
docker_ca=Path('/etc/docker/certs.d')/host/'ca.crt';docker_ca.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(ca,docker_ca)
p=Path('/etc/rancher/k3s/registries.yaml')
if p.exists():
 try:d=json.loads(p.read_text())
 except ValueError:raise RuntimeError('Existing YAML registry configuration needs explicit merge')
 shutil.copy2(p,str(p)+'.before-launchstead')
else:d={}
d.setdefault('configs',{})[host]={'auth':settings,'tls':{'ca_file':str(ca)}}
p.write_text(json.dumps(d,indent=2));p.chmod(0o600)
f=Path('/root/.docker/config.json');f.parent.mkdir(mode=0o700,exist_ok=True)
d=json.loads(f.read_text()) if f.exists() else {}
d.setdefault('auths',{})[host]={'auth':base64.b64encode((settings['username']+':'+settings['password']).encode()).decode()}
f.write_text(json.dumps(d,indent=2));f.chmod(0o600)
Path('/tmp/launchstead-registry-auth.json').unlink()
print('Private registry trust and credentials installed')
