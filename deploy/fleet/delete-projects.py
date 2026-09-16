#!/usr/bin/env python3
"""Finish owner-confirmed project deletion without touching other projects."""
import contextlib, fcntl, importlib.util, json, os, pwd, re, secrets, shutil, sqlite3, ssl, subprocess, time, urllib.request
from pathlib import Path

BASE = Path(__file__).resolve().parent

def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, BASE / filename)
    result = importlib.util.module_from_spec(spec); spec.loader.exec_module(result)
    return result

provision = module('provision', 'provision-projects.py')
domains = module('domains', 'reconcile-domains.py')
ID = re.compile(r'^[a-f0-9]{64}$')
DATABASE = Path('/var/lib/launchstead-portal/portal.sqlite')
FLEET = Path('/etc/launchstead-fleet/worker.json')
STATE = Path('/var/lib/launchstead-fleet')

@contextlib.contextmanager
def root():
    previous = os.geteuid()
    try:
        os.seteuid(0)
        yield
    finally:
        os.seteuid(previous)

def command(args):
    with root():
        return subprocess.run(args, check=True, capture_output=True, text=True, timeout=90)

def busy(db, project):
    return any(db.execute('SELECT 1 FROM '+table+" WHERE project_id=? AND state IN ('queued','running') LIMIT 1", (project,)).fetchone() for table in ('publication_jobs','node_builds','node_deployments'))

def remove_builder(project):
    # The assignment remains available until all external cleanup has succeeded.
    with root():
        assignment = provision.load(Path('/etc/launchstead-portal/node-builds')/(project+'.json'), None)
        legacy = provision.load(Path('/etc/launchstead-portal/node-demo-build.json'), None)
        if assignment is None and legacy and legacy.get('project') == project:
            assignment = legacy
            command(['systemctl','stop','node-demo-build.service'])
        elif assignment is not None:
            command(['systemctl','disable','--now','launchstead-node-build@'+project+'.service'])
        else:
            assignment = provision.load(Path('/etc/launchstead-fleet/build-template.json'), None)
        if assignment is None:
            raise RuntimeError('builder assignment unavailable')
        if assignment.get('project',project) != project:
            raise RuntimeError('builder assignment mismatch')
        context = ssl.create_default_context(cafile=assignment.get('ca_file') or None)
        request = urllib.request.Request(assignment['endpoint'].rstrip('/')+'/v1/project', method='DELETE', headers={
            'Authorization':'Bearer '+assignment['token'], 'X-Project-ID':project,
            'X-Toolchain-SHA256':assignment['toolchain_sha256'], 'X-Architecture':assignment['architecture']})
        with urllib.request.urlopen(request,context=context,timeout=60) as response:
            if response.status != 204:
                raise RuntimeError('builder cleanup incomplete')

def remove_primary_tls(project, hostname):
    app='site-'+project[:24]
    for kind,name in (('certificate',app+'-tls'),('secret',app+'-tls')):
        item=domains.get_optional(kind,name)
        if not item: continue
        labels=item.get('metadata',{}).get('labels',{})
        owned = labels.get('deployer.io/app') == app and labels.get('app.kubernetes.io/managed-by') == 'deployer'
        if kind == 'secret':
            annotations=item.get('metadata',{}).get('annotations',{})
            owned = owned or bool(hostname and annotations.get('cert-manager.io/certificate-name') == app+'-tls' and annotations.get('cert-manager.io/issuer-name') == 'deployer-letsencrypt' and annotations.get('cert-manager.io/alt-names') == hostname)
        if not owned: raise RuntimeError('primary TLS ownership conflict')
        domains.run_kubectl(['delete',kind,'-n',domains.NAMESPACE,name,'--ignore-not-found','--wait=true','--timeout=10s'])

def remove_private_files(project):
    # No browser-provided path is accepted. IDs are validated before entry.
    with root():
        config=Path('/etc/launchstead-portal/node-builds')/(project+'.json')
        config.unlink(missing_ok=True)
        legacy=Path('/etc/launchstead-portal/node-demo-build.json')
        value=provision.load(legacy,None)
        if value and value.get('project')==project: legacy.unlink()
        deps=Path('/var/lib/launchstead-portal/dependencies')/project
        if deps.exists(): shutil.rmtree(deps)
        for path in STATE.glob('site-'+project[:24]+'-*'):
            if path.is_file() and not path.is_symlink(): path.unlink()

def purge_records(db, project):
    with db:
        db.execute('BEGIN IMMEDIATE')
        pending=db.execute('SELECT workspace_id FROM projects WHERE id=? AND deletion_requested_at>0',(project,)).fetchone()
        if not pending: return
        if busy(db,project): raise RuntimeError('project operation still active')
        if db.execute('SELECT 1 FROM project_domains WHERE project_id=?',(project,)).fetchone(): raise RuntimeError('domains still attached')
        db.execute('DELETE FROM node_active_deployments WHERE project_id=?',(project,))
        db.execute('DELETE FROM node_deployment_releases WHERE deployment_id IN (SELECT id FROM node_deployments WHERE project_id=?)',(project,))
        db.execute('DELETE FROM node_deployments WHERE project_id=?',(project,))
        db.execute('DELETE FROM node_releases WHERE build_id IN (SELECT id FROM node_builds WHERE project_id=?)',(project,))
        db.execute('DELETE FROM node_builds WHERE project_id=?',(project,))
        db.execute('DELETE FROM publications WHERE project_id=?',(project,))
        db.execute('DELETE FROM publication_jobs WHERE project_id=?',(project,))
        db.execute('DELETE FROM uploads WHERE project_id=?',(project,))
        db.execute('INSERT INTO audit_events VALUES(?,?,?,?,?)',(secrets.token_hex(32),'project-deletion-worker',pending[0],'project.deleted:'+project,int(time.time())))
        db.execute('DELETE FROM projects WHERE id=? AND deletion_requested_at>0',(project,))

def delete_one(db, project, kind):
    if not ID.fullmatch(project) or kind not in ('node','static'): raise RuntimeError('invalid project')
    if busy(db,project): raise RuntimeError('project operation still active')
    with db:
        db.execute("UPDATE project_domains SET state='removing' WHERE project_id=?",(project,))
    for row in db.execute('SELECT id,project_id,hostname,state FROM project_domains WHERE project_id=?',(project,)).fetchall():
        domains.reconcile_row(db,row)
    with root(): config=provision.load(FLEET,None)
    if config is None: raise RuntimeError('fleet config unavailable')
    app='site-'+project[:24]
    source=domains.get_optional('ingress',app)
    if source:
        labels=source.get('metadata',{}).get('labels',{})
        if labels.get('deployer.io/app')!=app or labels.get('app.kubernetes.io/managed-by')!='deployer': raise RuntimeError('project ingress ownership conflict')
    if kind=='node': remove_builder(project)
    args=[config['deployer_binary'],'--config',config['deployer_config'],'--output','json']
    if config.get('context'): args+=['--context',config['context']]
    command(args+['delete','--yes',app])
    remove_primary_tls(project,config['projects'].get(project,{}).get('domain'))
    with root():
        config['projects'].pop(project,None)
        provision.atomic(FLEET,config,'launchstead-portal')
        for path in (Path('/etc/launchstead-portal/publication-sites.json'),Path('/etc/launchstead-portal/node-projects.json')):
            values=provision.load(path,{})
            values.pop(project,None)
            provision.atomic(path,values,'launchstead-portal')
    # Restart from the reduced assignment set before removing the portal row.
    command(['systemctl','restart','launchstead-fleet-worker.service'])
    remove_private_files(project)
    purge_records(db,project)

def main():
    if os.geteuid()!=0: raise RuntimeError('must start as root')
    os.umask(0o077)
    with contextlib.ExitStack() as stack:
        for path in (STATE/'provision.lock',STATE/'domains.lock'):
            lock=stack.enter_context(path.open('a+'))
            fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        account=pwd.getpwnam('launchstead-portal')
        os.setgroups([]);os.setegid(account.pw_gid);os.seteuid(account.pw_uid)
        with sqlite3.connect('file:'+str(DATABASE)+'?mode=rw',uri=True,timeout=5) as db:
            db.execute('PRAGMA foreign_keys=ON')
            for project,kind in db.execute('SELECT id,kind FROM projects WHERE deletion_requested_at>0').fetchall():
                try: delete_one(db,project,kind)
                except Exception as exc:
                    print('project cleanup retry: '+project+' '+type(exc).__name__,flush=True)
                    with db: db.execute('UPDATE projects SET deletion_error=? WHERE id=?',('Cleanup is retrying. Contact support if this persists.',project))

if __name__=='__main__': main()
