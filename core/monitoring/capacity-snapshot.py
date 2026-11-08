#!/usr/bin/env python3
"""Capacity snapshot for the Capacity Plan (docs/plans/capacity-right-size-then-grow-by-cost.md).

Usage:  python3 -I core/monitoring/capacity-snapshot.py <window>     e.g. 3d

Reads Prometheus through the API server's service proxy (kubectl, read-only)
and prints, per namespace and container (the max over its pods):

  ws MiB    max working set over the window
  req MiB   memory request now
  cpu m     max 5-minute CPU rate over the window, in millicores
  req m     CPU request now
  rst       restarts during the window
  oom       1 when a container's last termination in the window was OOMKilled

Then the workers' total: the sum of per-container peaks, that sum times 1.3
(the sizing rule's margin) and two workers' allocatable memory. The sum adds
peaks of different times and counts a moved pod twice, so it is an upper bound.

The window must not be older than the lowest sample, which is printed first:
Prometheus keeps 3 days on an emptyDir, and a restart of its pod starts again.
"""
import datetime
import json
import subprocess
import sys
import urllib.parse

PROXY = "/api/v1/namespaces/monitoring/services/kps-prometheus:9090/proxy/api/v1/query?query="
CONTAINERS = 'container!="",container!="POD"'
MIB = 2**20


def query(expr):
    out = subprocess.run(["kubectl", "get", "--raw", PROXY + urllib.parse.quote(expr)],
                         check=True, capture_output=True).stdout
    return json.loads(out)["data"]["result"]


def by_container(expr):
    return {(r["metric"].get("namespace"), r["metric"].get("pod"), r["metric"].get("container")):
            float(r["value"][1]) for r in query(expr)}


def scalar(expr):
    result = query(expr)
    return float(result[0]["value"][1]) if result else 0.0


def window_header(window):
    """The window line: the window must not reach before the lowest sample."""
    lowest = datetime.datetime.fromtimestamp(scalar("prometheus_tsdb_lowest_timestamp_seconds"), datetime.timezone.utc)
    return (f"window {window}, lowest sample {lowest:%Y-%m-%d %H:%M} UTC,"
            f" now {datetime.datetime.now(datetime.timezone.utc):%Y-%m-%d %H:%M} UTC")


def container_peaks(window):
    """Per (namespace, container), the max over its pods: a dict with ws_mib,
    req_mib, cpu_m, req_m, restarts and oom. vpa-compare.py reads it too."""
    ws = by_container(f"max_over_time(container_memory_working_set_bytes{{{CONTAINERS}}}[{window}])")
    cpu = by_container(f"max_over_time(rate(container_cpu_usage_seconds_total{{{CONTAINERS}}}[5m])[{window}:1m])")
    req_mem = by_container('kube_pod_container_resource_requests{resource="memory"}')
    req_cpu = by_container('kube_pod_container_resource_requests{resource="cpu"}')
    restarts = by_container(f"max_over_time(kube_pod_container_status_restarts_total[{window}])"
                            f" - min_over_time(kube_pod_container_status_restarts_total[{window}])")
    oom = by_container(f'max_over_time(kube_pod_container_status_last_terminated_reason{{reason="OOMKilled"}}[{window}])')

    groups = {}
    for key in ws:
        namespace, _, container = key
        groups.setdefault((namespace, container), []).append(key)

    def peak(values, keys):
        return max((values.get(k, 0.0) for k in keys), default=0.0)

    return {group: {"ws_mib": peak(ws, keys) / MIB, "req_mib": peak(req_mem, keys) / MIB,
                    "cpu_m": peak(cpu, keys) * 1000, "req_m": peak(req_cpu, keys) * 1000,
                    "restarts": sum(restarts.get(k, 0.0) for k in keys), "oom": peak(oom, keys)}
            for group, keys in groups.items()}


def main(window):
    print(window_header(window))
    print(f"{'namespace':22} {'container':34} {'ws MiB':>7} {'req MiB':>7} {'cpu m':>6} {'req m':>6} {'rst':>3} {'oom':>3}")
    for (namespace, container), p in sorted(container_peaks(window).items()):
        print(f"{namespace:22} {container:34}"
              f" {p['ws_mib']:7.0f} {p['req_mib']:7.0f}"
              f" {p['cpu_m']:6.0f} {p['req_m']:6.0f}"
              f" {p['restarts']:3.0f} {p['oom']:3.0f}")

    workers = f'node=~"k8swk.*",{CONTAINERS}'
    total = scalar(f"sum(max_over_time(container_memory_working_set_bytes{{{workers}}}[{window}]))") / MIB
    requested = scalar('sum(kube_pod_container_resource_requests{resource="memory",node=~"k8swk.*"})') / MIB
    nodes = query('kube_node_status_allocatable{resource="memory",node=~"k8swk.*"}')
    two = sorted(float(n["value"][1]) for n in nodes)[:2]
    print(f"\nworkers: requested {requested:.0f} MiB, sum of peaks {total:.0f} MiB, x1.3 {total * 1.3:.0f} MiB,"
          f" two smallest workers allocatable {sum(two) / MIB:.0f} MiB")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    main(sys.argv[1])
