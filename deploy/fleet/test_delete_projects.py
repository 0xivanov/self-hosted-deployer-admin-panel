#!/usr/bin/env python3
import importlib.util
import pathlib
import sqlite3
import unittest
from contextlib import nullcontext
from unittest import mock


def load_module():
    path = pathlib.Path(__file__).with_name("delete-projects.py")
    spec = importlib.util.spec_from_file_location("delete_projects", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


mod = load_module()


class DeleteProjectsTest(unittest.TestCase):
    def test_lock_contention_retries_until_available(self):
        with mock.patch.object(mod.fcntl, 'flock', side_effect=[BlockingIOError(), None]) as lock, mock.patch.object(mod.time, 'monotonic', return_value=1), mock.patch.object(mod.time, 'sleep'):
            self.assertTrue(mod.wait_for_lock(object(), 2))
            self.assertEqual(lock.call_count, 2)

    def test_lock_contention_has_bounded_wait(self):
        with mock.patch.object(mod.fcntl, 'flock', side_effect=BlockingIOError()), mock.patch.object(mod.time, 'monotonic', return_value=2), mock.patch.object(mod.time, 'sleep') as sleep:
            self.assertFalse(mod.wait_for_lock(object(), 2))
            sleep.assert_not_called()

    def setUp(self):
        self.db = sqlite3.connect(":memory:", isolation_level=None)
        self.db.execute("PRAGMA foreign_keys=ON")
        self.db.executescript("""
            CREATE TABLE projects(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, deletion_requested_at INTEGER NOT NULL, kind TEXT NOT NULL);
            CREATE TABLE project_domains(id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), hostname TEXT, state TEXT, message TEXT);
            CREATE TABLE uploads(id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id));
            CREATE TABLE publication_jobs(id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), upload_id TEXT REFERENCES uploads(id), state TEXT);
            CREATE TABLE publications(project_id TEXT PRIMARY KEY REFERENCES projects(id), job_id TEXT REFERENCES publication_jobs(id));
            CREATE TABLE node_builds(id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), state TEXT);
            CREATE TABLE node_releases(build_id TEXT PRIMARY KEY REFERENCES node_builds(id));
            CREATE TABLE node_deployments(id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), state TEXT);
            CREATE TABLE node_deployment_releases(deployment_id TEXT PRIMARY KEY REFERENCES node_deployments(id), release_id TEXT REFERENCES node_releases(build_id));
            CREATE TABLE node_active_deployments(project_id TEXT PRIMARY KEY REFERENCES projects(id), deployment_id TEXT REFERENCES node_deployments(id));
            CREATE TABLE audit_events(id TEXT, actor_id TEXT, workspace_id TEXT, action TEXT, created_at INTEGER);
        """)
        self.projects = {"a" * 64: "static", "b" * 64: "node"}
        for project, kind in self.projects.items():
            self.db.execute("INSERT INTO projects VALUES(?,?,?,?)", (project, "workspace-" + project[:1], 1, kind))

    def tearDown(self):
        self.db.close()

    def add_static_records(self, project):
        self.db.execute("INSERT INTO uploads VALUES(?,?)", ("upload-" + project[:1], project))
        self.db.execute("INSERT INTO publication_jobs VALUES(?,?,?,?)", ("job-" + project[:1], project, "upload-" + project[:1], "succeeded"))
        self.db.execute("INSERT INTO publications VALUES(?,?)", (project, "job-" + project[:1]))

    def add_node_records(self, project):
        self.db.execute("INSERT INTO uploads VALUES(?,?)", ("upload-" + project[:1], project))
        self.db.execute("INSERT INTO node_builds VALUES(?,?,?)", ("build-" + project[:1], project, "succeeded"))
        self.db.execute("INSERT INTO node_releases VALUES(?)", ("build-" + project[:1],))
        self.db.execute("INSERT INTO node_deployments VALUES(?,?,?)", ("deploy-" + project[:1], project, "succeeded"))
        self.db.execute("INSERT INTO node_deployment_releases VALUES(?,?)", ("deploy-" + project[:1], "build-" + project[:1]))
        self.db.execute("INSERT INTO node_active_deployments VALUES(?,?)", (project, "deploy-" + project[:1]))

    def test_purge_records_removes_static_and_node_records_only_for_target(self):
        static, node = self.projects
        self.add_static_records(static)
        self.add_node_records(node)
        mod.purge_records(self.db, static)
        self.assertIsNone(self.db.execute("SELECT 1 FROM projects WHERE id=?", (static,)).fetchone())
        self.assertIsNotNone(self.db.execute("SELECT 1 FROM projects WHERE id=?", (node,)).fetchone())
        self.assertIsNotNone(self.db.execute("SELECT 1 FROM node_builds WHERE project_id=?", (node,)).fetchone())
        mod.purge_records(self.db, node)
        self.assertIsNone(self.db.execute("SELECT 1 FROM projects WHERE id=?", (node,)).fetchone())
        for table in ("uploads", "node_builds", "node_releases", "node_deployments", "node_deployment_releases", "node_active_deployments"):
            self.assertEqual(self.db.execute("SELECT count(*) FROM " + table).fetchone()[0], 0, table)

    def test_purge_rejects_busy_or_attached_domains_without_deleting(self):
        project = next(iter(self.projects))
        self.add_static_records(project)
        self.db.execute("INSERT INTO project_domains VALUES(?,?,?,?,?)", ("domain", project, "one.example", "removing", ""))
        with self.assertRaisesRegex(RuntimeError, "domains still attached"):
            mod.purge_records(self.db, project)
        self.assertIsNotNone(self.db.execute("SELECT 1 FROM projects WHERE id=?", (project,)).fetchone())
        self.db.execute("DELETE FROM project_domains")
        self.db.execute("UPDATE publication_jobs SET state='running'")
        with self.assertRaisesRegex(RuntimeError, "operation still active"):
            mod.purge_records(self.db, project)
        self.assertIsNotNone(self.db.execute("SELECT 1 FROM uploads WHERE project_id=?", (project,)).fetchone())

    def test_delete_one_external_failure_does_not_purge_records(self):
        project = next(iter(self.projects))
        with mock.patch.object(mod, "root", return_value=nullcontext()), \
             mock.patch.object(mod.domains, "reconcile_row"), \
             mock.patch.object(mod.domains, "get_optional", return_value=None), \
             mock.patch.object(mod.provision, "load", return_value={"deployer_binary": "deployer", "deployer_config": "config", "projects": {project: {}}}), \
             mock.patch.object(mod, "command", side_effect=RuntimeError("external cleanup failed")):
            with self.assertRaisesRegex(RuntimeError, "external cleanup failed"):
                mod.delete_one(self.db, project, "static")
        self.assertIsNotNone(self.db.execute("SELECT 1 FROM projects WHERE id=?", (project,)).fetchone())

    def add_container_records(self, project, state="succeeded"):
        self.db.executescript("""
            CREATE TABLE IF NOT EXISTS container_releases(
                id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
                actor_id TEXT, request_key TEXT, revision INTEGER, input BLOB, image BLOB, created_at INTEGER);
            CREATE TABLE IF NOT EXISTS container_deployments(
                id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
                release_id TEXT NOT NULL REFERENCES container_releases(id), state TEXT);
        """)
        self.db.execute("INSERT INTO container_releases VALUES(?,?,?,?,?,?,?,?)",
                        ("release-" + project[:1], project, "actor", "request", 1, b"", b"", 1))
        self.db.execute("INSERT INTO container_deployments VALUES(?,?,?,?)",
                        ("container-deploy-" + project[:1], project, "release-" + project[:1], state))

    def test_purge_container_records_deletes_deployments_before_releases(self):
        project = next(iter(self.projects))
        self.projects[project] = "container"
        self.db.execute("UPDATE projects SET kind='container' WHERE id=?", (project,))
        self.add_container_records(project)
        other = "b" * 64
        self.add_container_records(other)
        mod.purge_records(self.db, project)
        self.assertEqual(self.db.execute("SELECT project_id FROM container_releases").fetchall(), [(other,)])
        self.assertEqual(self.db.execute("SELECT project_id FROM container_deployments").fetchall(), [(other,)])
        self.assertEqual(self.db.execute("SELECT id FROM projects").fetchall(), [(other,)])

    def test_purge_rejects_pending_container_deployment(self):
        project = next(iter(self.projects))
        self.db.execute("UPDATE projects SET kind='container' WHERE id=?", (project,))
        self.add_container_records(project, state="queued")
        with self.assertRaisesRegex(RuntimeError, "operation still active"):
            mod.purge_records(self.db, project)
        self.assertIsNotNone(self.db.execute("SELECT 1 FROM projects WHERE id=?", (project,)).fetchone())

    def test_schema42_without_container_tables_remains_compatible(self):
        project = next(iter(self.projects))
        mod.purge_records(self.db, project)
        self.assertIsNone(self.db.execute("SELECT 1 FROM projects WHERE id=?", (project,)).fetchone())

    def test_container_cleanup_removes_only_its_assignment_and_skips_builder(self):
        project, other = self.projects
        self.db.execute("UPDATE projects SET kind='container' WHERE id=?", (project,))
        self.add_container_records(project)
        config = {'deployer_binary':'deployer', 'deployer_config':'config', 'projects':{
            project:{'kind':'container','domain':'one.example'}, other:{'kind':'node','domain':'two.example'}}}
        saved = {}
        def load(path, default):
            return config if path == mod.FLEET else {project:{'runtime_id':'1'*64},other:{'runtime_id':'2'*64}}
        with mock.patch.object(mod,'root',side_effect=lambda:nullcontext()), mock.patch.object(mod.domains,'get_optional',return_value=None), mock.patch.object(mod.provision,'load',side_effect=load), mock.patch.object(mod.provision,'atomic',side_effect=lambda path,value,owner:saved.update({str(path):dict(value)})), mock.patch.object(mod,'command') as command, mock.patch.object(mod,'remove_builder') as builder, mock.patch.object(mod,'remove_primary_tls'), mock.patch.object(mod,'remove_private_files'):
            mod.delete_one(self.db,project,'container')
        builder.assert_not_called()
        mapping=saved[str(mod.provision.CONTAINER_PROJECTS)]
        self.assertNotIn(project,mapping)
        self.assertIn(other,mapping)
        self.assertIsNotNone(self.db.execute("SELECT 1 FROM projects WHERE id=?",(other,)).fetchone())
        self.assertIsNone(self.db.execute("SELECT 1 FROM projects WHERE id=?",(project,)).fetchone())
        self.assertTrue(any('delete' in call.args[0] for call in command.call_args_list))


if __name__ == "__main__":
    unittest.main()
