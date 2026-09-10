#!/usr/bin/env python3
"""Guest-only trusted runtime profile rehearsal. Never accepts customer artifacts."""
import hashlib
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request
import uuid

assert os.getuid() != 0 and os.uname().machine == 'aarch64'
base = Path('/opt/deployer-node')
toolchain = '5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7'
archive = Path('/opt/node-positive-lab/node.tar.xz')
assert hashlib.sha256(archive.read_bytes()).hexdigest() == toolchain
for command in (['getent','passwd','60000'], ['getent','group','60000']):
    found = subprocess.run(command, capture_output=True, text=True)
    assert found.returncode != 0 or found.stdout.split(':')[0] == 'dn-lab-runtime', 'reserved lab ID already belongs to another account'
if subprocess.run(['getent','group','60000'], capture_output=True).returncode:
    subprocess.run(['sudo','-n','groupadd','--gid','60000','dn-lab-runtime'],check=True)
if subprocess.run(['getent','passwd','60000'], capture_output=True).returncode:
    subprocess.run(['sudo','-n','useradd','--uid','60000','--gid','60000','--no-create-home','--shell','/usr/sbin/nologin','dn-lab-runtime'],check=True)
assert subprocess.check_output(['id','-G','dn-lab-runtime'],text=True).strip() == '60000'
# Establish a reachable non-loopback local endpoint before testing the unit's filter.
class Probe(http.server.BaseHTTPRequestHandler):
    calls = 0
    def do_GET(self):
        Probe.calls += 1
        self.send_response(200); self.end_headers(); self.wfile.write(b'baseline')
    def log_message(self,*args): pass
server = http.server.ThreadingHTTPServer(('0.0.0.0',0),Probe)
thread = threading.Thread(target=server.serve_forever,daemon=True);thread.start()
ip = subprocess.check_output(['hostname','-I'],text=True).split()[0]
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
with opener.open(f'http://{ip}:{server.server_port}/',timeout=2) as response: assert response.read() == b'baseline'
with socket.socket() as port_check:
    assert port_check.connect_ex(('127.0.0.1',31877)) != 0, 'lab runtime port is occupied'
with tempfile.TemporaryDirectory(prefix='runtime-unit-') as directory:
    stage = Path(directory)
    (stage/'server.mjs').write_bytes(Path('/tmp/node-runtime-probe.mjs').read_bytes())
    (stage/'package.json').write_text(json.dumps({'name':'runtime-unit-fixture','version':'1.0.0','type':'module','scripts':{'start':'node server.mjs','prestart':'node missing-prestart.mjs'}}))
    (stage/'probe.json').write_text(json.dumps({'host':ip,'port':server.server_port}))
    # Fixture identity covers the exact staged files; a production release uses
    # the validated exported archive and a durable operation/UID reservation.
    digest = hashlib.sha256(b''.join(p.name.encode()+b'\0'+p.read_bytes() for p in sorted(stage.iterdir()))).hexdigest()
    operation = uuid.uuid4().hex + uuid.uuid4().hex
    unit = json.loads(subprocess.check_output(['/tmp/node-runtime-unit',operation,digest],text=True))
    unit_path = stage/unit['Name'];unit_path.write_text(unit['Unit'])
    release = base/'releases'/('release-'+digest)
    tool = base/'toolchains'/toolchain
    subprocess.run(['sudo','-n','install','-d','-m','755',str(release),str(tool)],check=True)
    subprocess.run(['sudo','-n','tar','--no-same-owner','--strip-components=1','-xf',str(archive),'-C',str(tool)],check=True)
    for filename in ['server.mjs','package.json','probe.json']:
        subprocess.run(['sudo','-n','install','-m','444',str(stage/filename),str(release/filename)],check=True)
    subprocess.run(['sudo','-n','chmod','555',str(release)],check=True)
    empty=stage/'empty';empty.write_text('')
    for filename in ['empty-user.npmrc','empty-global.npmrc']:
        subprocess.run(['sudo','-n','install','-m','444',str(empty),str(base/filename)],check=True)
    canary=stage/'canary';canary.write_text('synthetic-root-only-canary')
    subprocess.run(['sudo','-n','install','-m','600',str(canary),str(base/'management-canary')],check=True)
    installed = '/run/systemd/system/'+unit['Name']
    subprocess.run(['sudo','-n','install','-m','644',str(unit_path),installed],check=True)
    try:
        subprocess.run(['sudo','-n','systemd-analyze','verify',installed],check=True,timeout=10)
        subprocess.run(['sudo','-n','systemctl','daemon-reload'],check=True,timeout=10)
        subprocess.run(['sudo','-n','systemctl','start',unit['Name']],check=True,timeout=20)
        report=None;deadline=time.monotonic()+12
        while time.monotonic()<deadline:
            try:
                with opener.open('http://127.0.0.1:31877/health',timeout=1) as response: report=json.load(response)
                break
            except OSError: time.sleep(.1)
        if not report:
            journal=subprocess.check_output(['sudo','-n','journalctl','-u',unit['Name'],'--no-pager','-o','cat','-n','30'],text=True)
            raise RuntimeError('runtime fixture did not become ready: '+journal)
        assert report['status']=='node-runtime-unit-ok' and all(report['checks'].values()), report
        assert Probe.calls==1, 'runtime reached the non-loopback probe server'
        for attempt in range(3):
            previous_pid = report['pid']
            subprocess.run(['sudo','-n','systemctl','kill','--kill-whom=main','--signal=KILL',unit['Name']],check=True,timeout=5)
            deadline=time.monotonic()+10
            recovered=False
            while time.monotonic()<deadline:
                if attempt==2:
                    state=subprocess.check_output(['sudo','-n','systemctl','show',unit['Name'],'--property=ActiveState','--property=MainPID','--property=NRestarts'],text=True)
                    if 'ActiveState=failed' in state and 'MainPID=0' in state and 'NRestarts=3' in state:
                        time.sleep(4)
                        stable=subprocess.check_output(['sudo','-n','systemctl','show',unit['Name'],'--property=ActiveState','--property=MainPID','--property=NRestarts'],text=True)
                        assert stable==state, 'failed service resumed after restart limit'
                        recovered=True;break
                else:
                    try:
                        with opener.open('http://127.0.0.1:31877/health',timeout=1) as response: current=json.load(response)
                        if current['pid']!=previous_pid and all(current['checks'].values()):
                            report=current;recovered=True;break
                    except OSError: pass
                time.sleep(.1)
            assert recovered, 'restart/restart-limit behavior did not match the service policy'
        assert Probe.calls==1, 'a restarted runtime bypassed network restriction'

    finally:
        subprocess.run(['sudo','-n','systemctl','stop',unit['Name']],check=True,timeout=10)
        state=subprocess.check_output(['sudo','-n','systemctl','show',unit['Name'],'--property=ActiveState','--property=MainPID'],text=True)
        assert ('ActiveState=inactive' in state or 'ActiveState=failed' in state) and 'MainPID=0' in state,state
        subprocess.run(['sudo','-n','rm',installed],check=True)
        subprocess.run(['sudo','-n','systemctl','daemon-reload'],check=True,timeout=10)
        subprocess.run(['sudo','-n','systemctl','reset-failed',unit['Name']],capture_output=True)
    with socket.socket() as check: assert check.connect_ex(('127.0.0.1',31877))!=0, 'runtime listener survived stop'
    print(json.dumps({'runtime_service':'passed','unit':unit['Name'],'report':report,'listener_stopped':True,'crash_restart':True,'restart_limit':True}),flush=True)
server.shutdown();server.server_close();thread.join(timeout=2)
