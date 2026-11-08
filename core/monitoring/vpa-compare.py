#!/usr/bin/env python3
"""Compare the VPA recommendations with the measured peaks (ADR 0014, Autoscaling Plan).

Usage:  python3 -I core/monitoring/vpa-compare.py <window>     e.g. 3d

Reads every VerticalPodAutoscaler (kubectl, read-only) and the per-container
peaks of capacity-snapshot.py over the window, and prints one row per
recommended container:

  peak      max working set over the window, MiB
  req       memory request now, MiB
  rule      the ADR 0013 request from that peak (x1.3, up to 16 Mi, at least 32 Mi)
  target    VPA's memory target, MiB; upper: its upper bound
  cpu pk    max 5-minute CPU rate over the window, millicores; req m: CPU request now
  cpu tg    VPA's CPU target, millicores

and flags a row when:

  MEM>2x, MEM<0.5x   the memory target is over twice or under half the request
                     (the drift band of ADR 0014)
  CPU>2x, CPU<0.5x   the same for CPU
  T<peak             the memory target is below the window's peak: often a start
                     peak, which a pod without a memory limit puts on the node

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


def recommendations():
    """(namespace, VPA name, container) -> (target MiB, upper MiB, target millicores).
    Keyed by the VPA too: two workloads in one namespace can share a container name."""
    out = subprocess.run(["kubectl", "get", "verticalpodautoscalers", "-A", "-o", "json"],
                         check=True, capture_output=True).stdout
    rows = {}
    for vpa in json.loads(out)["items"]:
        namespace, name = vpa["metadata"]["namespace"], vpa["metadata"]["name"]
        for c in vpa.get("status", {}).get("recommendation", {}).get("containerRecommendations", []):
            rows[(namespace, name, c["containerName"])] = (quantity(c["target"]["memory"]) / MIB,
                                                     quantity(c["upperBound"]["memory"]) / MIB,
                                                     quantity(c["target"]["cpu"]) * 1000)
    return rows


def sizing_rule(peak_mib):
    return max(32, math.ceil(peak_mib * 1.3 / 16) * 16)


def drift(target, request, name):
    if not request:
        return []
    ratio = target / request
    return [f"{name}>2x"] if ratio > DRIFT_HIGH else [f"{name}<0.5x"] if ratio < DRIFT_LOW else []


def main(window):
    snapshot = load_snapshot()
    print(snapshot.window_header(window))
    peaks = snapshot.container_peaks(window)
    print(f"{'namespace/vpa/container':64} {'peak':>5} {'req':>5} {'rule':>5} {'target':>6} {'upper':>6}"
          f" {'cpu pk':>6} {'req m':>5} {'cpu tg':>6}  flags")
    for (namespace, name, container), (target, upper, cpu_target) in sorted(recommendations().items()):
        # The peaks are per (namespace, container), the max over its pods: a
        # container name shared by two workloads shows the larger peak for both.
        p = peaks.get((namespace, container))
        label = f"{namespace}/{name}/{container}"
        if p is None:
            print(f"{label:64} {'-':>5} {'-':>5} {'-':>5} {target:6.0f} {upper:6.0f}"
                  f" {'-':>6} {'-':>5} {cpu_target:6.0f}  no peak in the window")
            continue
        flags = drift(target, p["req_mib"], "MEM") + drift(cpu_target, p["req_m"], "CPU")
        if target < p["ws_mib"]:
            flags.append("T<peak")
        print(f"{label:64} {p['ws_mib']:5.0f} {p['req_mib']:5.0f}"
              f" {sizing_rule(p['ws_mib']):5d} {target:6.0f} {upper:6.0f}"
              f" {p['cpu_m']:6.0f} {p['req_m']:5.0f} {cpu_target:6.0f}  {' '.join(flags)}")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    main(sys.argv[1])
