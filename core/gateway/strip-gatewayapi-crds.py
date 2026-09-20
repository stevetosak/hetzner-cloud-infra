#!/usr/bin/env python3
"""Removes Gateway API objects from a rendered Envoy Gateway manifest.

Reads a multi-document YAML stream on stdin and writes it back on stdout with
two classes of object removed:

  * CustomResourceDefinitions in group gateway.networking.k8s.io
  * CustomResourceDefinitions in group gateway.networking.x-k8s.io, which is
    where the experimental channel puts its extension kinds (xbackends,
    xbackendtrafficpolicies, xmeshes). The upstream safe-upgrade policy does
    NOT catch these — its CEL expression tests the k8s.io group alone — so
    this filter is the only thing that keeps them out.
  * the safe-upgrades ValidatingAdmissionPolicy and its binding

Both belong to core/gateway-api/crds.yaml, which installs the Gateway API
STANDARD channel. The Envoy Gateway chart ships the EXPERIMENTAL channel, and
ADR 0008 allows only the standard one.

The filter works on parsed documents, not on text, so a change in the chart's
formatting cannot make it silently miss a CRD. It prints what it removed to
stderr, and fails if it removed nothing, because that means the chart changed
shape and the assumption behind this script no longer holds.
"""
import sys

import yaml

GATEWAY_API_GROUPS = ("gateway.networking.k8s.io", "gateway.networking.x-k8s.io")
SAFE_UPGRADE_NAME = "safe-upgrades.gateway.networking.k8s.io"


def is_gateway_api_object(doc):
    if not isinstance(doc, dict) or "kind" not in doc:
        sys.exit(
            "strip-gatewayapi-crds.py got a document that is not a Kubernetes "
            f"object: {str(doc)[:200]}. Helm prints 'Pulled:' and 'Digest:' on "
            "stdout when it fetches an oci:// chart; render.sh pulls the chart "
            "first to keep that out of the stream."
        )
    kind = doc.get("kind")
    name = (doc.get("metadata") or {}).get("name", "")
    if kind == "CustomResourceDefinition":
        return (doc.get("spec") or {}).get("group") in GATEWAY_API_GROUPS
    if kind in ("ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"):
        return name.strip('"') == SAFE_UPGRADE_NAME
    return False


def main():
    docs = list(yaml.safe_load_all(sys.stdin))
    kept, removed = [], []
    for doc in docs:
        if doc is None:
            continue
        (removed if is_gateway_api_object(doc) else kept).append(doc)

    for doc in removed:
        name = (doc.get("metadata") or {}).get("name", "?")
        print(f"stripped {doc.get('kind')} {name}", file=sys.stderr)

    if not removed:
        sys.exit(
            "strip-gatewayapi-crds.py removed nothing. The chart no longer "
            "ships Gateway API CRDs, or it names them differently. Check "
            "before trusting the render."
        )

    yaml.safe_dump_all(kept, sys.stdout, default_flow_style=False, sort_keys=False)


if __name__ == "__main__":
    main()
