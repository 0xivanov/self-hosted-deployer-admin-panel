#!/usr/bin/env python3
"""Reconcile portal verified custom hostnames into owned Traefik routes.

The portal is the source of truth for verification.  This process only reads
rows whose state is verified or active and never accepts a hostname from the
command line or a shell command.
"""
import fcntl, ipaddress, json, os, re, sqlite3, subprocess, pwd
from pathlib import Path

DATABASE = Path(os.environ.get("PORTAL_DATABASE", "/var/lib/launchstead-portal/portal.sqlite"))
LOCK = Path(os.environ.get("DOMAIN_RECONCILE_LOCK", "/var/lib/launchstead-fleet/domains.lock"))
NAMESPACE = "deployer-apps"
HOST_SUFFIX = "159-195-146-26.sslip.io"
ID_RE = re.compile(r"^[a-f0-9]{64}$")
LABEL = "deployer.io/domain-project"
MANAGED = "app.kubernetes.io/managed-by"
KUBECTL = os.environ.get("K3S_KUBECTL", "k3s")

def valid_hostname(host):
    if host != host.strip().lower():
        return False
    if host.endswith("."):
        return False
    if not host or len(host) > 253 or any(host == x or host.endswith("."+x) for x in ("sslip.io", "localhost", "local", "internal", "test")) or host in ("0xivanov.dev", "admin.0xivanov.dev", "portal.0xivanov.dev", "deploy.0xivanov.dev", "money.0xivanov.dev"):
        return False
    try:
        ipaddress.ip_address(host)
        return False
    except ValueError:
        pass
    if "*" in host or not all(1 <= len(x) <= 63 and re.fullmatch(r"[a-z0-9](?:[a-z0-9-]*[a-z0-9])?", x) for x in host.split(".")):
        return False
    return "." in host

def run_kubectl(args, payload=None):
    cmd = [KUBECTL, "kubectl"] if KUBECTL == "k3s" else [KUBECTL]
    cmd += args
    # SQLite stays under the portal identity, only the fixed kubectl invocation
    # runs with the root identity retained by this operator-installed process.
    previous = os.geteuid()
    try:
        os.seteuid(0)
        return subprocess.run(cmd, input=payload, text=True, capture_output=True, check=True, timeout=15)
    finally:
        os.seteuid(previous)

def get_json(args):
    return json.loads(run_kubectl(args + ["-o", "json"]).stdout)

def owned(obj, project):
    labels = obj.get("metadata", {}).get("labels", {})
    return labels.get(LABEL) == project[:24] and labels.get(MANAGED) == "deployer"

def conflict(host, project, name):
    for item in get_json(["get", "ingress", "-A"]).get("items", []):
        if item.get("metadata", {}).get("name") == name and item.get("metadata", {}).get("namespace") == NAMESPACE and owned(item, project):
            continue
        for rule in item.get("spec", {}).get("rules", []):
            other = rule.get("host", "").lower().rstrip(".")
            if other == host or (other.startswith("*.") and host.endswith(other[1:])):
                return True
    return False

def desired_ingress(domain_id, project, host, source):
    app = "site-" + project[:24]
    name = "custom-" + domain_id[:24]
    source_annotations = source.get("metadata", {}).get("annotations", {})
    annotations = {}
    if "traefik.ingress.kubernetes.io/router.middlewares" in source_annotations:
        annotations["traefik.ingress.kubernetes.io/router.middlewares"] = source_annotations["traefik.ingress.kubernetes.io/router.middlewares"]
    annotations.update({"traefik.ingress.kubernetes.io/router.entrypoints": "websecure", "traefik.ingress.kubernetes.io/router.tls": "true"})
    rules = source.get("spec", {}).get("rules", [])
    if len(rules) != 1 or rules[0].get("http", {}).get("paths", [{}])[0].get("backend", {}).get("service", {}).get("name") != app:
        raise RuntimeError("source ingress backend is not the expected project service")
    service = rules[0]["http"]["paths"][0]["backend"]["service"]
    if service.get("port", {}).get("number") != 8080:
        raise RuntimeError("source ingress backend port is not the expected project port")
    return {"apiVersion":"networking.k8s.io/v1", "kind":"Ingress", "metadata":{"name":name,"namespace":NAMESPACE,"labels":{MANAGED:"deployer",LABEL:project[:24]},"annotations":annotations}, "spec":{"ingressClassName":"traefik", "tls":[{"hosts":[host],"secretName":name+"-tls"}], "rules":[{"host":host,"http":{"paths":[{"path":"/","pathType":"Prefix","backend":{"service":{"name":service["name"],"port":{"number":service["port"]["number"]}}}}]}}]}}

def desired_certificate(domain_id, project, host):
    name = "custom-" + domain_id[:24]
    return {"apiVersion":"cert-manager.io/v1", "kind":"Certificate", "metadata":{"name":name,"namespace":NAMESPACE,"labels":{MANAGED:"deployer",LABEL:project[:24]}}, "spec":{"secretName":name+"-tls","secretTemplate":{"labels":{MANAGED:"deployer",LABEL:project[:24]}},"issuerRef":{"name":"deployer-letsencrypt","kind":"ClusterIssuer"},"dnsNames":[host]}}

def source_ingress(project):
    app = "site-" + project[:24]
    return get_json(["get", "ingress", "-n", NAMESPACE, app])

def ready(domain_id, host):
    cert = get_optional("certificate", "custom-" + domain_id[:24])
    generation = cert.get("metadata", {}).get("generation")
    return (cert.get("spec", {}).get("dnsNames") == [host] and generation is not None
            and any(c.get("type") == "Ready" and c.get("status") == "True"
                    and c.get("observedGeneration") == generation
                    for c in cert.get("status", {}).get("conditions", [])))

def get_optional(kind, name):
    result = run_kubectl(["get", kind, "-n", NAMESPACE, name, "--ignore-not-found", "-o", "json"])
    return json.loads(result.stdout) if result.stdout.strip() else {}

def assert_owned(kind, name, project):
    obj = get_optional(kind, name)
    if obj and not owned(obj, project):
        raise RuntimeError("custom domain resource ownership conflict")
    return obj

def endpoint_ready(project):
    try:
        ep = get_json(["get", "endpoints", "-n", NAMESPACE, "site-" + project[:24]])
        return any(a for subset in ep.get("subsets", []) for a in subset.get("addresses", []))
    except (subprocess.CalledProcessError, json.JSONDecodeError):
        return False

def reconcile_row(db, row):
    rid, project, host, state = row
    if not ID_RE.fullmatch(rid) or not ID_RE.fullmatch(project) or not valid_hostname(host):
        raise RuntimeError("invalid custom domain record")
    name = "custom-" + rid[:24]
    if state == "removing":
        for kind, object_name in (("ingress",name),("certificate",name),("secret",name+"-tls")):
            obj = assert_owned(kind, object_name, project)
            if obj:
                run_kubectl(["delete",kind,"-n",NAMESPACE,object_name,"--ignore-not-found","--wait=true","--timeout=10s"])
        with db:
            db.execute("DELETE FROM project_domains WHERE id=? AND state='removing'", (rid,))
        return
    if state not in ("verified", "active"):
        return
    if conflict(host, project, name):
        with db:
            db.execute("UPDATE project_domains SET message=?,state='verified' WHERE id=? AND state IN ('verified','active')", ("hostname conflicts with another ingress", rid))
        return
    for kind, object_name in (("ingress",name),("certificate",name),("secret",name+"-tls")):
        assert_owned(kind, object_name, project)
    source = get_optional("ingress", "site-" + project[:24])
    if not source:
        with db:
            db.execute("UPDATE project_domains SET message=? WHERE id=? AND state IN ('verified','active')", ("Publish your website first to activate this domain.",rid))
        return
    labels = source.get("metadata", {}).get("labels", {})
    if labels.get(MANAGED) != "deployer" or labels.get("deployer.io/app") != "site-" + project[:24]:
        raise RuntimeError("source ingress ownership could not be established")
    run_kubectl(["apply", "--server-side", "--field-manager", "launchstead-domain-reconciler", "-f", "-"], json.dumps(desired_ingress(rid, project, host, source)))
    run_kubectl(["apply", "--server-side", "--field-manager", "launchstead-domain-reconciler", "-f", "-"], json.dumps(desired_certificate(rid, project, host)))
    new_state, message = ('active', 'active') if ready(rid, host) and endpoint_ready(project) else ('verified', 'waiting for certificate and service endpoints')
    with db:
        db.execute("UPDATE project_domains SET state=?,message=? WHERE id=? AND state IN ('verified','active')", (new_state, message, rid))

def main():
    if os.geteuid() != 0:
        raise RuntimeError("must start as root")
    os.umask(0o077)
    LOCK.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with LOCK.open("a+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        account = pwd.getpwnam("launchstead-portal")
        os.setgroups([])
        os.setegid(account.pw_gid)
        os.seteuid(account.pw_uid)
        with sqlite3.connect("file:" + str(DATABASE) + "?mode=rw", uri=True, timeout=20) as db:
            rows = db.execute("SELECT id,project_id,hostname,state FROM project_domains WHERE state IN ('verified','active','removing') ORDER BY id").fetchall()
            for row in rows:
                try:
                    reconcile_row(db, row)
                except Exception as exc:
                    # Do not leak kubectl output or certificate material to users.
                    print("domain reconciliation failed: " + row[0] + " " + type(exc).__name__, flush=True)
                    with db:
                        db.execute("UPDATE project_domains SET message=? WHERE id=?", ("Connection is retrying. Contact support if this persists.",row[0]))

if __name__ == "__main__":
    main()
