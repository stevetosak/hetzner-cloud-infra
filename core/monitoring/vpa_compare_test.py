"""Unit tests of vpa-compare.py: the Git value of each target kind and the
drift band. Run: python3 -I -m unittest discover -s core/monitoring -p '*_test.py'"""

import importlib.util
import pathlib
import unittest

_path = pathlib.Path(__file__).with_name("vpa-compare.py")
_spec = importlib.util.spec_from_file_location("vpa_compare", _path)
vc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(vc)


def workload(kind, namespace, name, containers, init_containers=()):
    return {"kind": kind, "metadata": {"namespace": namespace, "name": name},
            "spec": {"template": {"spec": {"containers": list(containers),
                                           "initContainers": list(init_containers)}}}}


def container(name, memory=None, cpu=None, **extra):
    requests = {k: v for k, v in (("memory", memory), ("cpu", cpu)) if v}
    return {"name": name, "resources": {"requests": requests} if requests else {}, **extra}


OBJECTS = vc.index_objects([
    workload("Deployment", "doma", "doma", [container("doma", "128Mi", "10m")],
             [container("migrate", "64Mi", "50m"),
              container("proxy", "32Mi", "5m", restartPolicy="Always")]),
    workload("DaemonSet", "kube-system", "kube-proxy", [container("kube-proxy")]),
    workload("StatefulSet", "monitoring", "prometheus-kps-prometheus",
             [container("prometheus", "1104Mi", "106m"), container("config-reloader", "32Mi", "10m")]),
    {"kind": "Cluster", "metadata": {"namespace": "pg-cluster", "name": "tosak-pg-cluster"},
     "spec": {"resources": {"requests": {"cpu": "28m", "memory": "384Mi"},
                            "limits": {"memory": "768Mi"}}}},
])


class TemplateRequests(unittest.TestCase):
    def test_deployment_counts_native_sidecars_only(self):
        got = vc.template_requests(OBJECTS, "doma", {"kind": "Deployment", "name": "doma"})
        self.assertEqual(got, {"doma": (128, 10), "proxy": (32, 5)})

    def test_container_without_requests(self):
        got = vc.template_requests(OBJECTS, "kube-system", {"kind": "DaemonSet", "name": "kube-proxy"})
        self.assertEqual(got, {"kube-proxy": (None, None)})

    def test_prometheus_reads_the_generated_statefulset(self):
        got = vc.template_requests(OBJECTS, "monitoring", {"kind": "Prometheus", "name": "kps-prometheus"})
        self.assertEqual(got, {"prometheus": (1104, 106), "config-reloader": (32, 10)})

    def test_cnpg_cluster_reads_spec_resources(self):
        got = vc.template_requests(OBJECTS, "pg-cluster", {"kind": "Cluster", "name": "tosak-pg-cluster"})
        self.assertEqual(got, {"postgres": (384, 28)})

    def test_missing_target(self):
        self.assertEqual(vc.template_requests(OBJECTS, "doma", {"kind": "Deployment", "name": "gone"}), {})
        self.assertEqual(vc.template_requests(OBJECTS, "monitoring",
                                              {"kind": "Alertmanager", "name": "kps-alertmanager"}), {})


class Drift(unittest.TestCase):
    def test_band(self):
        self.assertEqual(vc.drift(257, 128, "MEM"), ["MEM>2x"])
        self.assertEqual(vc.drift(256, 128, "MEM"), [])
        self.assertEqual(vc.drift(64, 128, "MEM"), [])
        self.assertEqual(vc.drift(63, 128, "MEM"), ["MEM<0.5x"])

    def test_no_git_value(self):
        self.assertEqual(vc.drift(64, None, "CPU"), [])
        self.assertEqual(vc.drift(64, 0, "CPU"), [])


class Recommendations(unittest.TestCase):
    def test_keyed_by_vpa_and_container_with_target_ref(self):
        ref = {"apiVersion": "apps/v1", "kind": "Deployment", "name": "doma"}
        vpas = [{"metadata": {"namespace": "doma", "name": "doma"}, "spec": {"targetRef": ref},
                 "status": {"recommendation": {"containerRecommendations": [
                     {"containerName": "doma", "target": {"memory": "93323264", "cpu": "11m"},
                      "upperBound": {"memory": "105Mi", "cpu": "20m"}}]}}},
                {"metadata": {"namespace": "doma", "name": "new"}, "spec": {"targetRef": ref}}]
        self.assertEqual(vc.recommendations(vpas), {("doma", "doma", "doma"): (89, 105, 11, ref)})


if __name__ == "__main__":
    unittest.main()
