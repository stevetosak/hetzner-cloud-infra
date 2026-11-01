#!/usr/bin/env bash
# Checks every Prometheus rule this directory deploys, and unit-tests the
# custom ones. CI runs it (rules-check.yml); run it before a commit too:
#
#   ./core/monitoring/check-rules.sh
#
# It takes the place of the operator's admission webhook, which is off
# (kube-prometheus-stack-values.yaml): a rule that does not parse would
# otherwise reach the cluster and only show as PrometheusRuleFailures.
#
# 1. Every PrometheusRule in the render and in rules/ is cut down to its
#    `spec` (a plain Prometheus rule file) as <metadata.name>.rules.yaml.
# 2. `promtool check rules` reads all of them.
# 3. `promtool test rules` runs rules/tests/*.test.yaml next to them; a test
#    names its rule file as <metadata.name>.rules.yaml.
#
# promtool comes from the Prometheus image the render deploys, read from the
# render itself, so a chart bump moves the check with it. Needs docker and
# python3 with PyYAML (as render.sh does).
set -euo pipefail

cd "$(dirname "$0")/../.."
DIR="core/monitoring"

IMAGE="$(grep -o -m1 'quay.io/prometheus/prometheus:[^"]*' "$DIR/kube-prometheus-stack.yaml")"
[ -n "$IMAGE" ] || { echo "no Prometheus image in the render" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

python3 - "$TMP" "$DIR/kube-prometheus-stack.yaml" "$DIR"/rules/*.yaml <<'EOF'
import sys, yaml
out, paths = sys.argv[1], sys.argv[2:]
names = set()
for path in paths:
    for doc in yaml.safe_load_all(open(path)):
        if not doc or doc.get("kind") != "PrometheusRule":
            continue
        name = doc["metadata"]["name"]
        if name in names:
            sys.exit(f"two PrometheusRules named {name}")
        names.add(name)
        with open(f"{out}/{name}.rules.yaml", "w") as f:
            yaml.safe_dump(doc["spec"], f, sort_keys=False)
print(f"{len(names)} PrometheusRules", file=sys.stderr)
EOF

cp "$DIR"/rules/tests/*.test.yaml "$TMP"/
chmod -R a+rX "$TMP"

promtool() {
  docker run --rm --user "$(id -u):$(id -g)" -v "$TMP:/work:ro" -w /work \
    --entrypoint /bin/promtool "$IMAGE" "$@"
}

cd "$TMP"
# The chart's rules (kps-*) are checked for syntax only: upstream ships a
# duplicate apiserver recording rule that the linter reports, and the deny
# list is the only lever over them. Our rules also pass the linter.
promtool check rules --lint=none kps-*.rules.yaml >/dev/null
echo "the chart's rules parse"
shopt -s extglob
promtool check rules --lint-fatal !(kps-*).rules.yaml
promtool test rules *.test.yaml
