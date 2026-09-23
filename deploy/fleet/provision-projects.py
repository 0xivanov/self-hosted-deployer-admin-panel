#!/usr/bin/env python3
"""Enroll static, Node and explicitly enabled container projects in the private worker fleet.

Assignments and credentials remain operator controlled. The portal enforces
workspace permissions and hosting entitlement before accepting jobs.
"""
import fcntl, hashlib, json, os, pwd, re, secrets, sqlite3, subprocess, tempfile
from pathlib import Path

DATABASE = Path(os.environ.get("PORTAL_DATABASE", "/var/lib/launchstead-portal/portal.sqlite"))
FLEET_CONFIG = Path(os.environ.get("FLEET_CONFIG", "/etc/launchstead-fleet/worker.json"))
PUBLICATION_SITES = Path(os.environ.get("PUBLICATION_SITES", "/etc/launchstead-portal/publication-sites.json"))
CONTAINER_PROJECTS = Path(os.environ.get("CONTAINER_PROJECTS", "/etc/launchstead-portal/container-projects.json"))
LOCK = Path(os.environ.get("PROVISION_LOCK", "/var/lib/launchstead-fleet/provision.lock"))
HOST_SUFFIX = "159-195-146-26.sslip.io"
MAX_PROJECTS = int(os.environ.get("FLEET_MAX_PROJECTS", "5"))
if not 1 <= MAX_PROJECTS <= 50:
    raise ValueError("FLEET_MAX_PROJECTS must be between 1 and 50")
ID_RE = re.compile(r"^[a-f0-9]{64}$")

def atomic(path, value, owner=None):
    path = Path(path); path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=".new-", dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        if owner:
            account = pwd.getpwnam(owner)
            os.fchown(fd, account.pw_uid, account.pw_gid)
        with os.fdopen(fd, "w") as out:
            json.dump(value, out, indent=2, sort_keys=True); out.write("\n"); out.flush(); os.fsync(out.fileno())
        os.replace(tmp, path)
        d = os.open(path.parent, os.O_DIRECTORY)
        try: os.fsync(d)
        finally: os.close(d)
    finally:
        if os.path.exists(tmp): os.unlink(tmp)

def load(path, default):
    if not path.exists(): return default
    with path.open() as f: return json.load(f)

def eligible(db, containers_enabled=False):
    # The portal queue already enforces the authoritative billing/hosting gate.
    # This controller only enrolls projects from the current schema.
    rows = db.execute("SELECT id,kind FROM projects WHERE deletion_requested_at=0 ORDER BY id").fetchall()
    result = []
    for project, kind in rows:
        if not ID_RE.fullmatch(project): raise RuntimeError("invalid project identity")
        if kind in ("static", "node") or (kind == "container" and containers_enabled): result.append((project, kind))
    return result

def enroll_container(project, fleet, sites, containers):
    """Repair this project's assignments using its retained runtime identity."""
    if not isinstance(project, str) or not ID_RE.fullmatch(project):
        raise RuntimeError("invalid container project identity")
    if fleet.get("enable_container_deployments") is not True:
        return False
    existing = fleet["projects"].get(project)
    if existing is None and len(fleet["projects"]) >= MAX_PROJECTS:
        return False
    if existing is not None and (existing.get("kind") != "container" or existing.get("architecture") != "arm64"):
        raise RuntimeError("container assignment kind or architecture conflict")
    recorded = containers.get(project, {})
    runtime = existing.get("runtime_id") if existing else recorded.get("runtime_id", secrets.token_hex(32))
    if not isinstance(runtime, str) or not ID_RE.fullmatch(runtime):
        raise RuntimeError("invalid container runtime identity")
    if recorded and recorded.get("runtime_id") != runtime:
        raise RuntimeError("container portal assignment conflict")
    if any(key != project and item.get("runtime_id") == runtime for key, item in fleet["projects"].items()):
        raise RuntimeError("container runtime is shared with another project")
    if any(key != project and item.get("runtime_id") == runtime for key, item in containers.items()):
        raise RuntimeError("container portal runtime is shared")
    host = existing["domain"] if existing else "site-" + project[:24] + "." + HOST_SUFFIX
    # Preserve the operator's established domain; never replace it during repair.
    assignment = dict(existing) if existing else {"kind":"container", "domain":host, "runtime_id":runtime, "architecture":"arm64"}
    portal_assignment = {"runtime_id":runtime}
    changed = existing != assignment or containers.get(project) != portal_assignment or sites.get(project) != "https://" + host
    fleet["projects"][project] = assignment
    containers[project] = portal_assignment
    sites[project] = "https://" + host
    return changed

def main():
    if os.geteuid() != 0: raise RuntimeError("must run as root")
    LOCK.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with LOCK.open("a+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        fleet = load(FLEET_CONFIG, None)
        if not fleet: raise RuntimeError("existing fleet configuration required")
        sites = load(PUBLICATION_SITES, {})
        containers = load(CONTAINER_PROJECTS, {})
        node_path=Path("/etc/launchstead-portal/node-projects.json")
        nodes=load(node_path,{})
        build_template=load(Path("/etc/launchstead-fleet/build-template.json"),None)
        with sqlite3.connect("file:" + str(DATABASE) + "?mode=ro", uri=True) as db:
            projects = eligible(db, fleet.get("enable_container_deployments") is True)
        static_count = sum(1 for p in fleet["projects"].values() if p.get("kind") == "static")
        changed = False
        pending_nodes = []
        for project, kind in projects:
            if kind == "container":
                if project in nodes: raise RuntimeError("container has an existing Node assignment")
                changed = enroll_container(project, fleet, sites, containers) or changed
                continue
            if kind == "node":
                existing=fleet["projects"].get(project)
                if existing and project in nodes:
                    continue
                if not build_template or (not existing and len(fleet["projects"])>=MAX_PROJECTS):
                    pending_nodes.append(project);continue
                runtime=existing.get("runtime_id") if existing else secrets.token_hex(32)
                pin=build_template["toolchain_sha256"]
                host=existing["domain"] if existing else "site-"+project[:24]+"."+HOST_SUFFIX
                assignment={"kind":"node","domain":host,"runtime_id":runtime,"toolchain_sha256":pin,"architecture":"arm64"}
                worker=dict(build_template);worker["project"]=project
                worker["dependencies_directory"]="/var/lib/launchstead-portal/dependencies/"+project
                account=pwd.getpwnam("launchstead-portal")
                directory=Path(worker["dependencies_directory"]);directory.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
                os.chown(directory.parent,account.pw_uid,account.pw_gid)
                directory.mkdir(mode=0o700,exist_ok=True);os.chown(directory,account.pw_uid,account.pw_gid)
                configdir=Path("/etc/launchstead-portal/node-builds");configdir.mkdir(mode=0o700,exist_ok=True);os.chown(configdir,account.pw_uid,account.pw_gid)
                atomic(configdir/(project+".json"),worker,"launchstead-portal")
                subprocess.run(["systemctl","enable","--now","launchstead-node-build@"+project+".service"],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
                fleet["projects"][project]=assignment
                nodes[project]={"runtime_id":runtime,"build":{"Settings":{"Architecture":"arm64"},"ToolchainSHA256":pin}}
                sites[project]="https://"+host
                changed=True
                continue
            if project in fleet["projects"]:
                if fleet["projects"][project].get("kind") != "static": raise RuntimeError("assignment kind conflict")
                continue
            if project in sites:
                host = sites[project].split("://", 1)[-1].split("/", 1)[0]
                if not host.startswith("site-" + project[:24] + "."):
                    raise RuntimeError("existing publication assignment conflicts with stable host")
                fleet["projects"][project] = {"kind": "static", "domain": host}
                static_count += 1
                changed = True
                continue
            if len(fleet["projects"]) >= MAX_PROJECTS: continue
            name = "site-" + project[:24]
            assignment = {"kind": "static", "domain": name + "." + HOST_SUFFIX}
            fleet["projects"][project] = assignment
            sites[project] = "https://" + assignment["domain"]
            static_count += 1; changed = True
        if changed:
            atomic(FLEET_CONFIG, fleet, "launchstead-portal")
            atomic(PUBLICATION_SITES, sites, "launchstead-portal")
            atomic(node_path,nodes,"launchstead-portal")
            atomic(CONTAINER_PROJECTS,containers,"launchstead-portal")
        fingerprint=hashlib.sha256(json.dumps(fleet,sort_keys=True).encode()).hexdigest()
        applied=LOCK.parent/"provision-applied.json"
        if load(applied,None)!=fingerprint:
            subprocess.run(["systemctl", "restart", "launchstead-fleet-worker.service"], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            atomic(applied,fingerprint)
        for project in pending_nodes:
            print(project + ": node runtime assignment pending", flush=True)

if __name__ == "__main__":
    try: main()
    except Exception:
        print("fleet provisioning incomplete; will retry", flush=True)
        raise
