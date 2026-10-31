#!/usr/bin/env python3
"""Splits a rendered multi-document manifest into its CRDs and the rest.

Usage:  split-crds.py CRDS_OUT [--drop-namespace NAME] < rendered > rest

Every CustomResourceDefinition goes to CRDS_OUT; every other document goes to
stdout. Only kluster owns CRDs (ADR 0010, Amendment 2026-10-22): the seed
applies each `core/<dir>/crds.yaml`, and no kustomize build may contain one,
because the `core` AppProject does not admit the kind.

`--drop-namespace NAME` also removes the Namespace NAME, for an upstream
release that declares its own namespace. The seed owns every Namespace, so the
directory carries that namespace as a file of its own.

Each document is classified by parsing it, so a change in the chart's
formatting cannot make the split silently miss a CRD. The documents are
written back as the original text, so the rest stays byte for byte what the
chart rendered and a review sees only what moved.

The script fails when it finds no CRD, or not the Namespace it was told to
drop: the release changed shape, and the assumption behind the render no
longer holds.
"""
import argparse
import re
import sys

import yaml

SEPARATOR = re.compile(r"^---[ \t]*$", re.MULTILINE)


class Loader(yaml.SafeLoader):
    """SafeLoader that reads a bare `=` as the string it is.

    YAML 1.1 resolves a plain `=` to the `value` type, which SafeLoader has no
    constructor for; the prometheus-operator CRDs list `=` as an enum value of
    a label matcher. Only classification uses the parsed form, so this cannot
    change what is written out.
    """


Loader.add_constructor(
    "tag:yaml.org,2002:value", lambda loader, node: loader.construct_scalar(node)
)


def documents(text):
    """Yields the raw text of each document, separators removed."""
    for chunk in SEPARATOR.split(text):
        if chunk.strip():
            yield chunk.strip("\n") + "\n"


def parse(chunk):
    docs = [d for d in yaml.load_all(chunk, Loader=Loader) if d is not None]
    if not docs:
        return None
    if len(docs) > 1 or not isinstance(docs[0], dict) or "kind" not in docs[0]:
        sys.exit(
            "split-crds.py got a document that is not one Kubernetes object: "
            f"{chunk[:200]!r}. Helm prints 'Pulled:' and 'Digest:' on stdout "
            "when it fetches an oci:// chart; render.sh pulls the chart first "
            "to keep that out of the stream."
        )
    return docs[0]


def name_of(doc):
    return (doc.get("metadata") or {}).get("name", "?")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("crds_out")
    parser.add_argument("--drop-namespace")
    args = parser.parse_args()

    crds, rest, dropped = [], [], []
    for chunk in documents(sys.stdin.read()):
        doc = parse(chunk)
        if doc is None:
            continue
        if doc["kind"] == "CustomResourceDefinition":
            crds.append(chunk)
        elif doc["kind"] == "Namespace" and name_of(doc) == args.drop_namespace:
            dropped.append(chunk)
        else:
            rest.append(chunk)

    if not crds:
        sys.exit(
            "split-crds.py found no CustomResourceDefinition. The release no "
            "longer ships CRDs, or names the kind differently. Check before "
            "trusting the render."
        )
    if args.drop_namespace and not dropped:
        sys.exit(
            f"split-crds.py found no Namespace {args.drop_namespace} to drop. "
            "Check the release before trusting the render."
        )

    with open(args.crds_out, "w") as out:
        out.write("---\n".join(crds))
    sys.stdout.write("---\n".join(rest))
    print(f"split {len(crds)} CRDs, {len(rest)} other documents", file=sys.stderr)


if __name__ == "__main__":
    main()
