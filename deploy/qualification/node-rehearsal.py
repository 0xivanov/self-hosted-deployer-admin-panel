#!/usr/bin/env python3
"""Run only the checked-in synthetic fixture in the disposable Linux lab.
This is NOT a sandbox/executor for customer uploads.
"""
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

source, digest, helper, node_bin = sys.argv[1:]
assert os.getuid() != 0, 'fixture must run as an unprivileged guest user'
assert hashlib.sha256(Path(source).read_bytes()).hexdigest() == digest
with tempfile.TemporaryDirectory(prefix='node-build-rehearsal-') as directory:
    root = Path(directory)
    root.chmod(0o700)
    prepared = json.loads(subprocess.check_output([helper, source, digest, str(root)], text=True))
    plan = prepared['Plan']
    app = root / prepared['Source']['Directory']
    home = root / 'home'
    home.mkdir(mode=0o700)
    user_config = home / 'user.npmrc'
    global_config = home / 'global.npmrc'
    user_config.write_text('')
    global_config.write_text('')
    env = {'PATH': node_bin + ':/usr/bin:/bin', 'HOME': str(home), 'CI': 'true',
           'npm_config_cache': str(root / 'cache'), 'npm_config_userconfig': str(user_config),
           'npm_config_globalconfig': str(global_config)}
    assert plan['source_sha256'] == digest and plan['architecture'] == 'arm64'
    for step in plan['steps']:
        subprocess.run([step['program'], *step['args']], cwd=app, env=env, check=True, timeout=60)
    assert (app / 'prepare.marker').is_file(), 'install hook did not run'
    assert (app / 'dist/server.mjs').is_file(), 'build output absent'
    # An ephemeral listener avoids collisions inside the disposable guest.
    with socket.socket() as probe:
        probe.bind(('127.0.0.1', 0))
        port = probe.getsockname()[1]
    runtime_env = {**env, 'NODE_ENV': 'production', 'PORT': str(port), 'HOST': '127.0.0.1'}
    start = plan['start']
    process = subprocess.Popen([start['program'], *start['args']], cwd=app, env=runtime_env,
                               start_new_session=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        deadline = time.monotonic() + 10
        response = None
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError('fixture exited before becoming healthy')
            try:
                with urllib.request.urlopen(f'http://127.0.0.1:{port}/health', timeout=1) as r:
                    response = json.load(r)
                break
            except OSError:
                time.sleep(0.1)
        assert response == {'status': 'node-build-lab-ok', 'environment': 'production'}, response
        assert not (app / 'unexpected-prestart.marker').exists(), 'prestart hook unexpectedly ran'
    finally:
        try:
            os.killpg(process.pid, signal.SIGTERM)
            process.wait(timeout=5)
        except (ProcessLookupError, subprocess.TimeoutExpired):
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=5)
    # Confirm the app listener stopped too, not just its npm parent.
    with socket.socket() as probe:
        assert probe.connect_ex(('127.0.0.1', port)) != 0, 'app survived process-group shutdown'
    # A broken build must be reported as failure, never success.
    (app / 'scripts/build.mjs').write_text('process.exit(42)\n')
    broken = subprocess.run(['npm', 'run', 'build'], cwd=app, env=env, timeout=10,
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    assert broken.returncode != 0, 'broken build was accepted'
    print(json.dumps({'result': 'passed', 'node': subprocess.check_output(['node', '--version'], env=env, text=True).strip(),
                      'npm': subprocess.check_output(['npm', '--version'], env=env, text=True).strip(),
                      'install_hook': True, 'build': True, 'http_ready': True, 'prestart_disabled': True,
                      'process_group_shutdown': True, 'broken_build_rejected': True}))
