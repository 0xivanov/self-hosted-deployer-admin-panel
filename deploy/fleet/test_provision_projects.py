import copy
import importlib.util
import pathlib
import sqlite3
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('fleet_provision', pathlib.Path(__file__).with_name('provision-projects.py'))
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

class ContainerProvisionTest(unittest.TestCase):
    def fixture(self):
        return 'a' * 64, {'enable_container_deployments': True, 'projects': {}}, {}, {}

    def test_disabled_and_full_fleet_do_not_accept_container(self):
        project, fleet, sites, containers = self.fixture()
        fleet['enable_container_deployments'] = False
        self.assertFalse(mod.enroll_container(project, fleet, sites, containers))
        self.assertEqual(fleet['projects'], {})
        fleet['enable_container_deployments'] = True
        fleet['projects']['b' * 64] = {'kind': 'static', 'domain': 'kept.example'}
        before = copy.deepcopy(fleet)
        with mock.patch.object(mod, 'MAX_PROJECTS', 1):
            self.assertFalse(mod.enroll_container(project, fleet, sites, containers))
        self.assertEqual(fleet, before)
        self.assertEqual(containers, {})

    def test_enrollment_and_partial_write_repair_keep_runtime_and_domain(self):
        project, fleet, sites, containers = self.fixture()
        self.assertTrue(mod.enroll_container(project, fleet, sites, containers))
        original = copy.deepcopy(fleet['projects'][project])
        self.assertEqual(containers[project]['runtime_id'], original['runtime_id'])
        self.assertFalse(mod.enroll_container(project, fleet, sites, containers))
        fleet['projects'][project]['domain'] = 'client.example'
        containers.clear()
        sites.clear()
        with mock.patch.object(mod, 'MAX_PROJECTS', 1):
            self.assertTrue(mod.enroll_container(project, fleet, sites, containers))
        self.assertEqual(fleet['projects'][project]['runtime_id'], original['runtime_id'])
        self.assertEqual(sites[project], 'https://client.example')
        self.assertEqual(containers[project]['runtime_id'], original['runtime_id'])

    def test_conflicting_or_shared_runtime_is_rejected_without_changes(self):
        project, fleet, sites, containers = self.fixture()
        mod.enroll_container(project, fleet, sites, containers)
        containers[project]['runtime_id'] = 'c' * 64
        before = copy.deepcopy((fleet, sites, containers))
        with self.assertRaises(RuntimeError):
            mod.enroll_container(project, fleet, sites, containers)
        self.assertEqual((fleet, sites, containers), before)
        containers[project]['runtime_id'] = fleet['projects'][project]['runtime_id']
        fleet['projects']['d' * 64] = dict(fleet['projects'][project])
        with self.assertRaises(RuntimeError):
            mod.enroll_container(project, fleet, sites, containers)

    def test_existing_schema_and_deletion_eligibility(self):
        db = sqlite3.connect(':memory:')
        db.execute('CREATE TABLE projects(id TEXT,kind TEXT,deletion_requested_at INTEGER)')
        for char, kind, deleting in [('a','static',0),('b','node',0),('c','container',0),('d','container',1)]:
            db.execute('INSERT INTO projects VALUES(?,?,?)', (char*64,kind,deleting))
        self.assertEqual([kind for _,kind in mod.eligible(db)], ['static','node'])
        self.assertEqual([kind for _,kind in mod.eligible(db,True)], ['static','node','container'])
        db.close()

if __name__ == '__main__':
    unittest.main()
