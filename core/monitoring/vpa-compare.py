#!/usr/bin/env python3
"""Compare the VPA recommendations with the measured peaks (ADR 0014, Autoscaling Plan).

Usage:  python3 -I core/monitoring/vpa-compare.py <window>     e.g. 3d

Reads every VerticalPodAutoscaler and the pod template of its target
(kubectl, read-only), and the per-container peaks of capacity-snapshot.py over
the window, and prints one row per recommended container:

  peak      max working set over the window, MiB
  req       memory request of the running pod, MiB: VPA's value once it resizes
  git       memory request of the workload's template, MiB: the Git value, which
            Argo CD keeps; git m is the same for CPU
  rule      the ADR 0013 request from that peak (x1.3, up to 16 Mi, at least 32 Mi)
  target    VPA's memory target, MiB; upper: its upper bound
  cpu pk    max 5-minute CPU rate over the window, millicores; req m: CPU request now
  cpu tg    VPA's CPU target, millicores

and flags a row when:

  MEM>2x, MEM<0.5x   the memory target is over twice or under half the Git
                     value (the drift band of ADR 0014): raise or lower it in Git
  CPU>2x, CPU<0.5x   the same for CPU
  T<peak             the memory target is below the window's peak: often a start
                     peak, which a pod without a memory limit puts on the node

The template is the target's own for a Deployment, StatefulSet or DaemonSet;
for Prometheus and Alertmanager it is the StatefulSet their operator makes from
the custom resource; for a CloudNativePG Cluster it is `spec.resources`, the
container `postgres`. Kubernetes has no metric of a template's request, so the
drift has no alert (Autoscaling Plan); this script is where it shows.

The recommender keeps its history in checkpoints, not Prometheus: it knows only
what it saw since it started. Compare once it has run for the whole window.
"""

import importlib.util
import json
import math
import pathlib
import re
import subprocess
import sys

QUANTITY = re.compile(r"^([0-9.]+)([a-zA-Z]*)$")
# Suffix -> factor to bytes (memory) or to cores (CPU); the Kubernetes quantity suffixes.
SUFFIX = {"": 1, "m": 1e-3, "k": 1e3, "M": 1e6, "G": 1e9,
          "Ki": 2**10, "Mi": 2**20, "Gi": 2**30}
MIB = 2**20
DRIFT_HIGH, DRIFT_LOW = 2.0, 0.5


def load_snapshot():
    """capacity-snapshot.py, imported by path: its name is not a module name."""
    path = pathlib.Path(__file__).with_name("capacity-snapshot.py")
    spec = importlib.util.spec_from_file_location("capacity_snapshot", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def quantity(text):
    number, suffix = QUANTITY.match(str(text)).groups()
    return float(number) * SUFFIX[suffix]


# Kinds whose operator renders the pods through a StatefulSet of a fixed name.
GENERATED_STATEFULSET = {"Prometheus": "prometheus-{}", "Alertmanager": "alertmanager-{}"}
# The kinds a VPA targets, and what holds their templates.
TEMPLATE_KINDS = "deployments,statefulsets,daemonsets,clusters.postgresql.cnpg.io"


def kubectl_items(kinds):
    out = subprocess.run(["kubectl", "get", kinds, "-A", "-o", "json"],
                         check=True, capture_output=True).stdout
    return json.loads(out)["items"]


def recommendations(vpas):
    """(namespace, VPA name, container) -> (target MiB, upper MiB, target millicores,
    targetRef). Keyed by the VPA too: two workloads in one namespace can share a
    container name."""
    rows = {}
    for vpa in vpas:
        namespace, name = vpa["metadata"]["namespace"], vpa["metadata"]["name"]
        target_ref = vpa["spec"]["targetRef"]
        for c in vpa.get("status", {}).get("recommendation", {}).get("containerRecommendations", []):
            rows[(namespace, name, c["containerName"])] = (quantity(c["target"]["memory"]) / MIB,
                                                     quantity(c["upperBound"]["memory"]) / MIB,
                                                     quantity(c["target"]["cpu"]) * 1000,
                                                     target_ref)
    return rows


def index_objects(items):
    """(kind, namespace, name) -> object."""
    return {(o["kind"], o["metadata"]["namespace"], o["metadata"]["name"]): o for o in items}


def template_requests(objects, namespace, target_ref):
    """container -> (memory MiB, CPU millicores) of the target's pod template; a
    value the template does not set is None. {} when the template is not found."""
    kind, name = target_ref["kind"], target_ref["name"]
    if kind in GENERATED_STATEFULSET:
        kind, name = "StatefulSet", GENERATED_STATEFULSET[kind].format(name)
    obj = objects.get((kind, namespace, name))
    if obj is None:
        return {}
    if kind == "Cluster":
        containers = [{"name": "postgres", "resources": obj["spec"].get("resources", {})}]
    else:
        spec = obj["spec"]["template"]["spec"]
        # A native sidecar is an init container that keeps running.
        containers = spec["containers"] + [c for c in spec.get("initContainers", [])
                                           if c.get("restartPolicy") == "Always"]
    result = {}
    for c in containers:
        requests = c.get("resources", {}).get("requests", {})
        memory, cpu = requests.get("memory"), requests.get("cpu")
        result[c["name"]] = (quantity(memory) / MIB if memory else None,
                             quantity(cpu) * 1000 if cpu else None)
    return result


def sizing_rule(peak_mib):
    return max(32, math.ceil(peak_mib * 1.3 / 16) * 16)


def drift(target, request, name):
    if not request:
        return []
    ratio = target / request
    return [f"{name}>2x"] if ratio > DRIFT_HIGH else [f"{name}<0.5x"] if ratio < DRIFT_LOW else []


def cell(value, width):
    return f"{'-':>{width}}" if value is None else f"{value:{width}.0f}"


def main(window):
    snapshot = load_snapshot()
    print(snapshot.window_header(window))
    peaks = snapshot.container_peaks(window)
    objects = index_objects(kubectl_items(TEMPLATE_KINDS))
    print(f"{'namespace/vpa/container':64} {'peak':>5} {'req':>5} {'git':>5} {'rule':>5}"
          f" {'target':>6} {'upper':>6} {'cpu pk':>6} {'req m':>5} {'git m':>5} {'cpu tg':>6}  flags")
    rows = recommendations(kubectl_items("verticalpodautoscalers"))
    for (namespace, name, container), (target, upper, cpu_target, target_ref) in sorted(rows.items()):
        git_mib, git_m = template_requests(objects, namespace, target_ref).get(container, (None, None))
        flags = drift(target, git_mib, "MEM") + drift(cpu_target, git_m, "CPU")
        # The peaks are per (namespace, container), the max over its pods: a
        # container name shared by two workloads shows the larger peak for both.
        p = peaks.get((namespace, container), {})
        peak = p.get("ws_mib")
        if peak is None:
            flags.append("no peak in the window")
        elif target < peak:
            flags.append("T<peak")
        print(f"{namespace + '/' + name + '/' + container:64} {cell(peak, 5)} {cell(p.get('req_mib'), 5)}"
              f" {cell(git_mib, 5)} {cell(None if peak is None else sizing_rule(peak), 5)} {target:6.0f} {upper:6.0f}"
              f" {cell(p.get('cpu_m'), 6)} {cell(p.get('req_m'), 5)} {cell(git_m, 5)} {cpu_target:6.0f}"
              f"  {' '.join(flags)}")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    main(sys.argv[1])
