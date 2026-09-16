#!/usr/bin/env python3
import importlib.util, json, pathlib, sqlite3, subprocess, unittest
from unittest import mock

path = pathlib.Path(__file__).with_name("reconcile-domains.py")
spec = importlib.util.spec_from_file_location("reconcile_domains", path)
mod = importlib.util.module_from_spec(spec); spec.loader.exec_module(mod)

class DomainReconcilerTest(unittest.TestCase):
    def test_hostname_rules(self):
        self.assertTrue(mod.valid_hostname("www.customer.example"))
        for value in ("site-a.159-195-146-26.sslip.io", "159.1.2.3", "*.example.com", "customer.example.com.", "bad domain.example"):
            self.assertFalse(mod.valid_hostname(value))

    def test_manifest_keeps_original_service_and_uses_owned_name(self):
        project = "a" * 64
        source = {"metadata":{"annotations":{"traefik.ingress.kubernetes.io/router.middlewares":"deployer-apps-site@kubernetescrd"}, "labels":{"app.kubernetes.io/managed-by":"deployer","deployer.io/app":"site-" + project[:24]}}, "spec":{"rules":[{"http":{"paths":[{"backend":{"service":{"name":"site-" + project[:24],"port":{"number":8080}}}}]}}]}}
        domain_id = "b" * 64
        ingress = mod.desired_ingress(domain_id, project, "www.customer.example", source)
        self.assertEqual(ingress["metadata"]["name"], "custom-" + domain_id[:24])
        backend = ingress["spec"]["rules"][0]["http"]["paths"][0]["backend"]["service"]
        self.assertEqual(backend, {"name":"site-" + project[:24], "port":{"number":8080}})
        self.assertEqual(ingress["metadata"]["labels"][mod.LABEL], project[:24])
        self.assertTrue(all(len(v) <= 63 for v in ingress["metadata"]["labels"].values()))
        cert = mod.desired_certificate(domain_id, project, "www.customer.example")
        self.assertEqual(cert["spec"]["secretName"], ingress["spec"]["tls"][0]["secretName"])

    def test_multiple_domains_on_one_project_get_distinct_owned_resources(self):
        project = "a" * 64
        source = {"metadata":{"labels":{"app.kubernetes.io/managed-by":"deployer","deployer.io/app":"site-" + project[:24]}}, "spec":{"rules":[{"http":{"paths":[{"backend":{"service":{"name":"site-" + project[:24],"port":{"number":8080}}}}]}}]}}
        first = mod.desired_ingress("b" * 64, project, "one.customer.example", source)
        second = mod.desired_ingress("c" * 64, project, "two.customer.example", source)
        self.assertNotEqual(first["metadata"]["name"], second["metadata"]["name"])
        self.assertEqual(first["spec"]["rules"][0]["host"], "one.customer.example")
        self.assertEqual(second["spec"]["rules"][0]["host"], "two.customer.example")

    def test_ready_rejects_stale_generation(self):
        with mock.patch.object(mod, "get_optional", return_value={"metadata":{"generation":3}, "spec":{"dnsNames":["one.customer.example"]}, "status":{"conditions":[{"type":"Ready","status":"True","observedGeneration":2}]}}):
            self.assertFalse(mod.ready("b" * 64, "one.customer.example"))

    def test_ready_rejects_different_hostname(self):
        with mock.patch.object(mod, "get_optional", return_value={"metadata":{"generation":3}, "spec":{"dnsNames":["other.customer.example"]}, "status":{"conditions":[{"type":"Ready","status":"True","observedGeneration":3}]}}):
            self.assertFalse(mod.ready("b" * 64, "one.customer.example"))

    def test_ready_accepts_matching_observed_generation(self):
        with mock.patch.object(mod, "get_optional", return_value={"metadata":{"generation":3}, "spec":{"dnsNames":["one.customer.example"]}, "status":{"conditions":[{"type":"Ready","status":"True","observedGeneration":3}]}}):
            self.assertTrue(mod.ready("b" * 64, "one.customer.example"))

    def test_wrong_source_ownership_refuses_before_apply(self):
        db = sqlite3.connect(":memory:")
        db.execute("CREATE TABLE project_domains(id TEXT, project_id TEXT, hostname TEXT, state TEXT, message TEXT)")
        project = "a" * 64
        wrong = {"metadata":{"labels":{"app.kubernetes.io/managed-by":"someone-else","deployer.io/app":"site-" + project[:24]}}}
        with mock.patch.object(mod, "conflict", return_value=False), mock.patch.object(mod, "get_optional", return_value=wrong), mock.patch.object(mod, "run_kubectl") as apply:
            with self.assertRaises(RuntimeError):
                mod.reconcile_row(db, ("b" * 64, project, "one.customer.example", "verified"))
            apply.assert_not_called()

    def test_cleanup_failure_keeps_removing_row(self):
        db = sqlite3.connect(":memory:")
        db.execute("CREATE TABLE project_domains(id TEXT, project_id TEXT, hostname TEXT, state TEXT, message TEXT)")
        rid, project = "b" * 64, "a" * 64
        db.execute("INSERT INTO project_domains VALUES(?,?,?,?,?)", (rid, project, "one.customer.example", "removing", ""))
        owned = {"metadata":{"labels":{mod.LABEL:project[:24], mod.MANAGED:"deployer"}}}
        failure = subprocess.CalledProcessError(1, "k3s", stderr="temporary API failure")
        with mock.patch.object(mod, "get_optional", return_value=owned), mock.patch.object(mod, "run_kubectl", side_effect=failure):
            with self.assertRaises(subprocess.CalledProcessError):
                mod.reconcile_row(db, (rid, project, "one.customer.example", "removing"))
        self.assertEqual(db.execute("SELECT count(*) FROM project_domains WHERE id=?", (rid,)).fetchone()[0], 1)

if __name__ == "__main__":
    unittest.main()
