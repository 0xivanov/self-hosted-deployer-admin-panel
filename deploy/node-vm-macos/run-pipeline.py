#!/usr/bin/python3
"""Run one bounded build/export pipeline using the disposable Mac VMs."""
import errno
import fcntl
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import stat
import subprocess
import sys
import time

MIN_DISK = 64 << 20
MAX_SOURCE = 10 << 20
MAX_DEPENDENCIES = 100 << 20


def require(condition, message):
    if not condition:
        raise ValueError(message)


def private(path, directory=None):
    path = Path(path)
    info = path.lstat()
    require(info.st_uid == os.getuid() and not (info.st_mode & 0o077), "private path required")
    if directory is True:
        require(stat.S_ISDIR(info.st_mode), "private directory required")
    elif directory is False:
        require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1, "private regular file required")
    return info


def digest(value):
    require(isinstance(value, str) and len(value) == 64 and all(c in "0123456789abcdef" for c in value), "invalid digest")
    return value


def read_json(path, keys):
    info = private(path, False)
    require(info.st_size <= 16384, "metadata too large")
    with Path(path).open("rb") as source:
        value = json.loads(source.read(16385))
    require(isinstance(value, dict) and set(value) == set(keys), "unexpected JSON fields")
    return value


def sync_file(path, data):
    path = Path(path)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        view = memoryview(data)
        while view:
            written = os.write(fd, view)
            require(written > 0, "short record write")
            view = view[written:]
        os.fsync(fd)
    finally:
        os.close(fd)
    directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def copy_regular(source, destination):
    private(source, False)
    destination = Path(destination)
    with Path(source).open("rb") as src, destination.open("xb") as dst:
        shutil.copyfileobj(src, dst, 1 << 20)
        dst.flush()
        os.fsync(dst.fileno())
    destination.chmod(0o600)


def clone_disk(source, destination):
    # macOS APFS copy-on-write clone avoids materializing a full OS image and
    # consuming the short build lease. Fail closed when cloning is unavailable.
    private(source, False)
    require(not Path(destination).exists(), "disk destination already exists")
    subprocess.run(["/bin/cp", "-c", str(source), str(destination)], check=True, timeout=10)
    os.chmod(destination, 0o600)
    with Path(destination).open("rb") as copied:
        os.fsync(copied.fileno())


def copy_tree(source, destination, limit):
    private(source, True)
    destination = Path(destination)
    destination.mkdir(mode=0o700)
    total = 0
    count = 0
    for root, dirs, files in os.walk(source, topdown=True, followlinks=False):
        root = Path(root)
        dirs.sort()
        files.sort()
        for name in dirs + files:
            count += 1
            require(count <= 20000, "too many dependency entries")
            path = root / name
            info = path.lstat()
            require(not stat.S_ISLNK(info.st_mode) and (stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)), "unsafe dependency entry")
            relative = path.relative_to(source)
            require(len(relative.parts) <= 64, "dependency tree too deep")
            target = destination / relative
            if stat.S_ISDIR(info.st_mode):
                target.mkdir(mode=0o700)
            else:
                total += info.st_size
                require(total <= limit, "dependency bundle too large")
                copy_regular(path, target)
    return total


def run(args):
    require(len(args) == 2, "usage: run-pipeline.py PRIVATE_CONFIG_JSON PRIVATE_EXECUTION_DIRECTORY")
    config = read_json(args[0], ("TemplateDirectory", "DependenciesDirectory", "Launcher", "Importer"))
    for key in ("TemplateDirectory", "DependenciesDirectory"):
        value = config[key]
        require(isinstance(value, str) and os.path.isabs(value), "absolute configured path required")
        private(value, True)
    for key in ("Launcher", "Importer"):
        value = config[key]
        require(isinstance(value, str) and os.path.isabs(value), "absolute configured executable required")
        info = private(value, False)
        require(info.st_mode & 0o111, "configured executable required")
    execution = Path(args[1])
    require(execution.is_absolute() and str(execution.resolve()) == str(execution), "canonical execution path required")
    private(execution, True)
    lock = os.open(execution, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError as error:
        os.close(lock)
        if error.errno in (errno.EACCES, errno.EAGAIN):
            raise ValueError("execution is already claimed")
        raise
    try:
        request = read_json(execution / "request.json", ("ExecutionID", "BuildID", "ProjectID", "Plan", "ToolchainSHA256", "Bundle", "NotAfter"))
        execution_id = digest(request["ExecutionID"])
        digest(request["BuildID"])
        digest(request["ProjectID"])
        plan = request["Plan"]
        require(isinstance(plan, dict) and set(plan) == {"version", "source_sha256", "os", "architecture", "node_major", "steps", "start", "port"}, "invalid plan")
        source = digest(plan["source_sha256"])
        require(plan["version"] == 1 and plan["os"] == "linux" and plan["architecture"] in ("arm64", "amd64") and plan["node_major"] == 24 and isinstance(plan["steps"], list) and isinstance(plan["start"], dict) and isinstance(plan["port"], int) and not isinstance(plan["port"], bool) and 1024 <= plan["port"] <= 65535, "invalid plan")
        digest(request["ToolchainSHA256"])
        bundle = request["Bundle"]
        require(isinstance(bundle, dict) and set(bundle) == {"Directory", "ManifestSHA256"}, "invalid bundle")
        digest(bundle["ManifestSHA256"])
        bundle_name = bundle["Directory"]
        require(isinstance(bundle_name, str) and bundle_name.startswith("dependencies-") and bundle_name[13:] == digest(bundle_name[13:]), "invalid dependency name")
        require(isinstance(request["NotAfter"], int) and not isinstance(request["NotAfter"], bool) and time.time() < request["NotAfter"] <= time.time() + 60, "invalid deadline")
        source_path = execution / "source.zip"
        source_info = private(source_path, False)
        require(0 < source_info.st_size <= MAX_SOURCE, "source archive out of bounds")
        source_hash = hashlib.sha256()
        with source_path.open("rb") as source_file:
            for chunk in iter(lambda: source_file.read(1 << 20), b""):
                source_hash.update(chunk)
        require(source_hash.hexdigest() == source, "source identity mismatch")

        sync_file(execution / "pipeline-attempt.json", json.dumps({"ExecutionID": execution_id, "BuildID": request["BuildID"], "started_at": time.time()}, sort_keys=True, separators=(",", ":")).encode())
        deadline = request["NotAfter"]
        build = execution / "build"
        build.mkdir(mode=0o700)
        template = Path(config["TemplateDirectory"])
        clone_disk(template / "disk", build / "disk")
        copy_regular(template / "efi", build / "efi")
        input_tree = build / "input"
        input_tree.mkdir(mode=0o700)
        copy_regular(source_path, input_tree / "source.zip")
        guest_request = {key: request[key] for key in ("ExecutionID", "Plan", "ToolchainSHA256", "Bundle", "NotAfter")}
        sync_file(input_tree / "request.json", json.dumps(guest_request, sort_keys=True, separators=(",", ":")).encode())
        copy_tree(Path(config["DependenciesDirectory"]) / bundle_name, input_tree / bundle_name, MAX_DEPENDENCIES)
        subprocess.run(["/usr/bin/hdiutil", "makehybrid", "-o", str(build / "input.iso"), "-udf", "-udf-volume-name", "DEPLOYER_BUILD", str(input_tree)], check=True, timeout=30)
        os.chmod(build / "input.iso", 0o600)
        with (build / "output.disk").open("xb") as output:
            os.fchmod(output.fileno(), 0o600)
            output.truncate(MIN_DISK * 2)
            output.flush()
            os.fsync(output.fileno())
        remaining = int(deadline - time.time())
        require(remaining >= 1, "build deadline elapsed before launch")
        subprocess.run([config["Launcher"], str(build), execution_id, str(remaining), "--build-disks"], check=True, timeout=remaining + 15)
        verify_receipts(build, execution_id, 3)

        export = execution / "export"
        export.mkdir(mode=0o700)
        clone_disk(template / "disk", export / "disk")
        copy_regular(template / "efi", export / "efi")
        clone_disk(build / "output.disk", export / "snapshot.disk")
        os.chmod(export / "snapshot.disk", 0o400)
        snapshot_hash = file_hash(export / "snapshot.disk")
        export_input = export / "metadata"
        export_input.mkdir(mode=0o700)
        metadata = {"ExecutionID": execution_id, "SourceSHA256": source, "SnapshotSHA256": snapshot_hash, "NotAfter": int(time.time()) + 55}
        sync_file(export_input / "request.json", json.dumps(metadata, sort_keys=True, separators=(",", ":")).encode())
        subprocess.run(["/usr/bin/hdiutil", "makehybrid", "-o", str(export / "input.iso"), "-udf", "-udf-volume-name", "DEPLOYER_EXPORT", str(export_input)], check=True, timeout=30)
        os.chmod(export / "input.iso", 0o600)
        with (export / "output.disk").open("xb") as output:
            os.fchmod(output.fileno(), 0o600)
            output.truncate(MIN_DISK * 2)
            output.flush()
            os.fsync(output.fileno())
        export_id = secrets.token_hex(32)
        subprocess.run([config["Launcher"], str(export), export_id, "45", "--export-disks"], check=True, timeout=60)
        verify_receipts(export, export_id, 4)
        result = subprocess.run([config["Importer"], str(export / "output.disk"), str(execution / "release.zip"), execution_id, source, snapshot_hash], check=True, capture_output=True, text=True, timeout=60)
        manifest = json.loads(result.stdout)
        artifact = digest(manifest["SHA256"])
        output = {"ExecutionID": execution_id, "BuildID": request["BuildID"], "ProjectID": request["ProjectID"], "SourceSHA256": source, "ToolchainSHA256": request["ToolchainSHA256"], "Architecture": plan["architecture"], "DependencyManifestSHA256": bundle["ManifestSHA256"], "ArtifactSHA256": artifact, "SnapshotSHA256": snapshot_hash, "ExportOperationID": export_id, "Outcome": "succeeded"}
        sync_file(execution / "result.json", json.dumps(output, sort_keys=True, separators=(",", ":")).encode())
    finally:
        os.close(lock)


def file_hash(path):
    private(path, False)
    value = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            value.update(chunk)
    return value.hexdigest()


def verify_receipts(directory, operation, storage):
    running = read_json(Path(directory) / "running.json", ("operation", "observed_at", "network_devices", "storage_devices", "build_disks", "export_disks"))
    stopped = read_json(Path(directory) / "stopped.json", ("operation", "observed_at", "state", "build_disks", "export_disks"))
    require(running["operation"] == operation and running["network_devices"] == 0 and running["storage_devices"] == storage, "invalid running receipt")
    require(stopped["operation"] == operation and stopped["state"] == "stopped", "invalid stopped receipt")
    for receipt in [running, stopped]:
        require(receipt["export_disks"] == (storage == 4) and receipt["build_disks"] == (storage == 3), "incorrect VM mode")
    require(0 < running["observed_at"] <= stopped["observed_at"] <= time.time(), "invalid receipt time")


if __name__ == "__main__":
    try:
        run(sys.argv[1:])
    except Exception as error:
        print("pipeline failed:", error, file=sys.stderr)
        sys.exit(1)
