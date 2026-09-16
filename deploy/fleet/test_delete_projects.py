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


if __name__ == "__main__":
    unittest.main()
