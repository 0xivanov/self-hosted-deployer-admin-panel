#!/usr/bin/env python3
"""Reconcile a bounded, operator-enabled static hosting preview on the VPS."""
import fcntl
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import sqlite3
import subprocess
import tempfile

BASE = Path('/var/lib/launchstead-provisioner')
PORTAL = Path('/etc/launchstead-portal')
DATABASE = '/var/lib/launchstead-portal/portal.sqlite'
MAPPING = PORTAL / 'publication-sites.json'
MAX_SITES = 5
NETWORK = '10.8.0.1'
SUFFIX = '159-195-146-26.sslip.io'


def run(*args, data=None):
    return subprocess.run(args, input=data, text=True, check=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout


def atomic(path, value, owner='root'):
    path = Path(path)
    uid = pwd.getpwnam(owner).pw_uid
    gid = pwd.getpwnam(owner).pw_gid
    fd, temporary = tempfile.mkstemp(dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        os.fchown(fd, uid, gid)
        with os.fdopen(fd, 'w') as out:
            out.write(json.dumps(value, indent=2) + '\n')
            out.flush()
            os.fsync(out.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def unit(user, command, writable):
    return f'''[Unit]
Description=Launchstead automatically assigned static project
After=network-online.target
Wants=network-online.target
[Service]
User={user}
Group={user}
ExecStart={command}
Restart=on-failure
RestartSec=10
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=
ReadWritePaths={writable}
MemoryMax=128M
TasksMax=32
[Install]
WantedBy=multi-user.target
'''


def resources(name, host, port):
    ns = 'launchstead-sites'
    meta = {'name': name, 'namespace': ns}
    return {'apiVersion': 'v1', 'kind': 'List', 'items': [
        {'apiVersion': 'v1', 'kind': 'Service', 'metadata': {**meta, 'annotations': {
            'traefik.ingress.kubernetes.io/service.serversscheme': 'https',
            'traefik.ingress.kubernetes.io/service.serverstransport': f'{ns}-{name}@kubernetescrd'}},
         'spec': {'ports': [{'name': 'https', 'port': port, 'targetPort': port}]}},
        {'apiVersion': 'discovery.k8s.io/v1', 'kind': 'EndpointSlice',
         'metadata': {**meta, 'labels': {'kubernetes.io/service-name': name,
                                      'endpointslice.kubernetes.io/managed-by': 'launchstead-provisioner'}},
         'addressType': 'IPv4', 'ports': [{'name': 'https', 'protocol': 'TCP', 'port': port}],
         'endpoints': [{'addresses': [NETWORK], 'conditions': {'ready': True}}]},
        {'apiVersion': 'traefik.io/v1alpha1', 'kind': 'ServersTransport', 'metadata': meta,
         'spec': {'serverName': host, 'rootCAsSecrets': [name + '-origin-ca']}},
        {'apiVersion': 'networking.k8s.io/v1', 'kind': 'Ingress',
         'metadata': {**meta, 'annotations': {'cert-manager.io/cluster-issuer': 'deployer-letsencrypt',
             'traefik.ingress.kubernetes.io/router.entrypoints': 'websecure',
             'traefik.ingress.kubernetes.io/router.tls': 'true'}},
         'spec': {'ingressClassName': 'traefik', 'rules': [{'host': host, 'http': {'paths': [
             {'path': '/', 'pathType': 'Prefix', 'backend': {'service': {'name': name, 'port': {'number': port}}}}]}}],
             'tls': [{'hosts': [host], 'secretName': name + '-public-tls'}]}}
    ]}


def provision(project, slot):
    name = 'site-' + project[:24]
    user = 'ls-' + project[:16]
    host = name + '.' + SUFFIX
    state = BASE / project
    state.mkdir(mode=0o700, exist_ok=True)
    try:
        pwd.getpwnam(user)
    except KeyError:
        run('useradd', '--system', '--no-create-home', '--shell', '/usr/sbin/nologin', user)
    config = Path('/etc/launchstead-sites') / project
    content = Path('/var/lib/launchstead-sites') / project
    for directory in (config, content):
        run('install', '-d', '-m', '0700', '-o', user, '-g', user, str(directory))
    cert, key = config / 'tls.crt', config / 'tls.key'
    if not cert.exists() or not key.exists():
        run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '365',
            '-subj', '/CN=' + host, '-addext', 'subjectAltName=DNS:' + host + ',IP:127.0.0.1',
            '-keyout', str(key), '-out', str(cert))
        for file in (cert, key):
            os.chmod(file, 0o600)
            os.chown(file, pwd.getpwnam(user).pw_uid, pwd.getpwnam(user).pw_gid)
    content_port, management_port = 8900 + slot * 2, 8901 + slot * 2
    runtime = config / 'config.json'
    if runtime.exists():
        token = json.loads(runtime.read_text())['token']
    else:
        token = secrets.token_hex(32)
    atomic(runtime, {'root': str(content), 'project': project, 'token': token,
        'content_listen': f'{NETWORK}:{content_port}', 'content_host': host,
        'management_listen': f'127.0.0.1:{management_port}', 'management_host': f'127.0.0.1:{management_port}',
        'content_cert': str(cert), 'content_key': str(key),
        'management_cert': str(cert), 'management_key': str(key), 'spa': False}, user)
    ca = PORTAL / (name + '-ca.crt')
    # Public certificate only, never copy the runtime private key to the portal.
    ca.write_text(cert.read_text())
    os.chmod(ca, 0o600)
    os.chown(ca, pwd.getpwnam('launchstead-portal').pw_uid, pwd.getpwnam('launchstead-portal').pw_gid)
    assignment = PORTAL / (name + '.json')
    atomic(assignment, {'project': project, 'endpoint': f'https://127.0.0.1:{management_port}',
                       'token': token, 'ca_file': str(ca)}, 'launchstead-portal')
    units = {
        name: unit(user, f'/opt/launchstead-static/static-runtime --config {runtime}', str(content)),
        name + '-worker': unit('launchstead-portal', f'/opt/launchstead-portal/publication-worker --database {DATABASE} --assignment {assignment}', '/var/lib/launchstead-portal')}
    for service, body in units.items():
        path = Path('/etc/systemd/system') / (service + '.service')
        path.write_text(body)
        path.chmod(0o644)
    run('systemctl', 'daemon-reload')
    run('systemctl', 'enable', '--now', name, name + '-worker')
    namespace = run('k3s', 'kubectl', 'create', 'namespace', 'launchstead-sites', '--dry-run=client', '-o', 'json')
    run('k3s', 'kubectl', 'apply', '-f', '-', data=namespace)
    secret = run('k3s', 'kubectl', 'create', 'secret', 'generic', name + '-origin-ca', '-n', 'launchstead-sites',
                 '--from-file=ca.crt=' + str(cert), '--dry-run=client', '-o', 'json')
    run('k3s', 'kubectl', 'apply', '-f', '-', data=secret)
    run('k3s', 'kubectl', 'apply', '-f', '-', data=json.dumps(resources(name, host, content_port)))
    run('systemctl', 'is-active', name, name + '-worker')
    certificate = json.loads(run('k3s', 'kubectl', 'get', 'certificate', name + '-public-tls',
                                '-n', 'launchstead-sites', '-o', 'json'))
    if not any(c.get('type') == 'Ready' and c.get('status') == 'True'
               for c in certificate.get('status', {}).get('conditions', [])):
        print(f'{project}: waiting for HTTPS certificate', flush=True)
        return None
    backup = Path('/etc/deployer/backup/recovery.json')
    settings = json.loads(backup.read_text())
    for service in units:
        path = '/etc/systemd/system/' + service + '.service'
        if path not in settings['paths']:
            settings['paths'].append(path)
    atomic(backup, settings)
    return 'https://' + host


def main():
    if os.geteuid() != 0:
        raise RuntimeError('must run as root')
    BASE.mkdir(mode=0o700, exist_ok=True)
    with (BASE / 'lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        registry = BASE / 'assignments.json'
        assigned = json.loads(registry.read_text()) if registry.exists() else {}
        mapping = json.loads(MAPPING.read_text())
        with sqlite3.connect('file:' + DATABASE + '?mode=ro', uri=True, timeout=5) as db:
            projects = [row[0] for row in db.execute("SELECT id FROM projects WHERE kind='static' ORDER BY id")]
        for project in projects:
            if not re.fullmatch('[a-f0-9]{64}', project):
                raise RuntimeError('invalid project identity')
            if project in mapping:
                continue  # Keep legacy assignments and active deployments unchanged.
            if project not in assigned:
                if len(assigned) >= MAX_SITES:
                    print('Static preview capacity reached', flush=True)
                    continue
                if any(p[:24] == project[:24] or p[:16] == project[:16] for p in assigned):
                    raise RuntimeError('project identity prefix collision')
                assigned[project] = len(assigned)
                atomic(registry, assigned)
            try:
                origin = provision(project, assigned[project])
                if origin:
                    mapping[project] = origin
                    atomic(MAPPING, mapping, 'launchstead-portal')
                    print(f'{project}: ready at {origin}', flush=True)
            except subprocess.CalledProcessError:
                # Commands may contain private configuration paths. Do not log output.
                print(f'{project}: provisioning incomplete; will retry', flush=True)


if __name__ == '__main__':
    main()
