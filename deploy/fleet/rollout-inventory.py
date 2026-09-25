#!/usr/bin/env python3
"""Read-only, secret-free inventory before/after a coordinated VPS upgrade.

Run on the VPS as root. This is an observation, not a backup or rollout approval.
No configurations, environment variables, customer rows or credentials are printed.
"""
import argparse
import hashlib
import json
import sqlite3
import subprocess
from datetime import datetime, timezone
from pathlib import Path


def database(path, tables, jobs, core=False):
    with sqlite3.connect(Path(path).resolve().as_uri() + '?mode=ro', uri=True, timeout=5) as db:
        db.execute('PRAGMA query_only=ON')
        db.execute('BEGIN')  # All counts and integrity checks use one snapshot.
        present = {r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        if core:
            row = db.execute('SELECT version_id FROM goose_db_version WHERE is_applied=1 ORDER BY id DESC LIMIT 1').fetchone()
            version = row[0] if row else None
        else:
            version = db.execute('PRAGMA user_version').fetchone()[0]
        counts = {t: db.execute('SELECT COUNT(*) FROM "' + t + '"').fetchone()[0]
                  for t in tables if t in present}
        pending = {t: db.execute('SELECT COUNT(*) FROM "' + t + '" WHERE "' + column + '" IN (' + ','.join('?' for _ in states) + ')', states).fetchone()[0]
                   for t, column, states in jobs if t in present}
        # Events remain labelled pending after processing. Use the processing
        # record, including events not yet claimed, instead of that label.
        if {'github_push_events', 'github_push_processing'} <= present:
            pending['github_push_processing'] = db.execute('''SELECT COUNT(*)
                FROM github_push_events e LEFT JOIN github_push_processing q ON q.event_id=e.id
                WHERE q.event_id IS NULL OR q.state='running' ''').fetchone()[0]
        if {'github_push_events', 'github_imports', 'github_pipelines'} <= present:
            pending['github_pipelines_uncreated'] = db.execute('''SELECT COUNT(*)
                FROM github_push_events e JOIN github_imports i
                ON i.request_key='github-push:'||e.id AND i.project_id=e.project_id
                WHERE i.state='succeeded' AND i.upload_id IS NOT NULL
                AND NOT EXISTS(SELECT 1 FROM github_pipelines p WHERE p.event_id=e.id)''').fetchone()[0]
        return {'schema': version, 'counts': counts, 'unfinished': pending,
                'integrity_ok': db.execute('PRAGMA quick_check').fetchall() == [('ok',)],
                'foreign_keys_ok': db.execute('PRAGMA foreign_key_check').fetchone() is None}


def command(args):
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=45).stdout


def fleet():
    nodes = json.loads(command(['k3s', 'kubectl', 'get', 'nodes', '-o', 'json']))['items']
    deployments = json.loads(command(['k3s', 'kubectl', '-n', 'deployer-apps', 'get', 'deployments', '-o', 'json']))['items']
    return {
        'nodes': [{'name': n['metadata']['name'],
                   'ready': any(c['type'] == 'Ready' and c['status'] == 'True' for c in n.get('status', {}).get('conditions', []))}
                  for n in nodes],
        'deployments': [{'name': d['metadata']['name'], 'desired': d['spec'].get('replicas', 1),
                         'available': d.get('status', {}).get('availableReplicas', 0),
                         'updated': d.get('status', {}).get('updatedReplicas', 0),
                         'observed': d.get('status', {}).get('observedGeneration', 0) >= d['metadata']['generation']}
                        for d in deployments]}


def binaries():
    paths = [Path('/opt/launchstead-portal') / name for name in (
        'customer-portal', 'billing-worker', 'node-build-worker',
        'node-deployment-worker', 'publication-worker', 'billing-plan')]
    paths += [Path('/opt/launchstead-fleet/fleet-worker'), Path('/opt/launchstead-fleet/fleet-logs'), Path('/opt/deployer-admin-panel/deployer')]
    # Hash the running core binary rather than guessing its installation path.
    pid = command(['systemctl', 'show', 'deployer-server.service', '--property=MainPID', '--value']).strip()
    if not pid.isdecimal() or int(pid) < 1:
        raise RuntimeError('core process unavailable')
    sources = [(str(p), p) for p in paths]
    sources.append(('deployer-server.service:running', Path('/proc') / pid / 'exe'))
    results = {}
    for label, path in sources:
        if not path.is_file():
            results[label] = None
            continue
        digest = hashlib.sha256()
        with path.open('rb') as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(block)
        results[label] = digest.hexdigest()
    return results


def database_consumer_units():
    # Older/static provisioners use different unit names. Discover their command
    # paths without exposing any command lines or configuration values.
    names = set()
    raw = command(['systemctl', 'show', '*.service', '--property=Id,ExecStart', '--no-pager'])
    for block in raw.split('\n\n'):
        props = dict(line.split('=', 1) for line in block.splitlines() if '=' in line)
        start = props.get('ExecStart', '')
        if '/opt/launchstead-portal/' in start or '/var/lib/launchstead-portal/portal.sqlite' in start:
            names.add(props['Id'])
    return sorted(names)


def units():
    patterns = ['customer-portal.service', 'deployer-server.service', 'deployer-admin-panel.service',
                'launchstead-*.service', 'fleet-*.service', 'fleet-*.timer', 'node-demo-*.service',
                'project-deletion.*', 'domain-reconciler.*']
    return json.loads(command(['systemctl', 'list-units', '--all', '--no-pager', '--output=json',
                               *patterns, *database_consumer_units()]))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--portal-db', default='/var/lib/launchstead-portal/portal.sqlite')
    parser.add_argument('--core-db', default='/var/lib/deployer/deployer.db')
    args = parser.parse_args()
    report = {'observed_at': datetime.now(timezone.utc).isoformat(), 'errors': []}
    checks = {
        'portal': lambda: database(args.portal_db,
            ('users', 'sessions', 'projects', 'uploads', 'publications', 'node_releases', 'project_domains', 'container_releases',
             'github_connections', 'github_imports', 'github_push_events', 'github_pipelines'),
            [(t, 'state', ('queued', 'running')) for t in ('publication_jobs', 'node_builds', 'node_deployments', 'container_deployments', 'github_imports')]
            + [('github_pipelines', 'state', ('waiting', 'building', 'publishing'))]),
        'core': lambda: database(args.core_db, ('apps', 'deployments', 'routes', 'deployment_requests'),
            [('deployment_requests', 'state', ('pending',))], core=True),
        'fleet': fleet,
        'binaries': binaries,
        'units': units,
    }
    for name, check in checks.items():
        try:
            result = check()
            if name == 'units':
                result = [{k: u.get(k) for k in ('unit', 'load', 'active', 'sub')} for u in result]
            report[name] = result
        except Exception as error:
            # Raw command stderr and DB errors can include sensitive values.
            report['errors'].append({'check': name, 'type': type(error).__name__})
    print(json.dumps(report, indent=2, sort_keys=True))
    return 1 if report['errors'] else 0


if __name__ == '__main__':
    raise SystemExit(main())
