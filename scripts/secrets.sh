#!/bin/bash
# The one way Secrets reach the cluster (ADR 0003). Every Secret is a whole
# Kubernetes manifest, `<name>.enc.yaml`, committed beside its blank template
# `<name>.yaml` and encrypted with SOPS + age. Only `data` and `stringData` are
# encrypted (.sops.yaml), so key names stay readable in review.
#
#   scripts/secrets.sh edit <file.enc.yaml>        open in $SOPS_EDITOR, create if new
#   scripts/secrets.sh encrypt <file.enc.yaml>     encrypt a Secret manifest read on stdin
#   scripts/secrets.sh apply <file|dir>...         decrypt and apply server-side
#   scripts/secrets.sh diff <file|dir>...          show drift from the cluster, values masked
#   scripts/secrets.sh check [--staged]            fail on any .enc.yaml that is not encrypted
#
# `encrypt` is for values that already live in a file or in the cluster, so the
# plaintext never touches disk:
#
#   kubectl create secret generic keystore -n authos \
#     --from-file=keystore.p12=/etc/keystore/authos-cluster-keystore.p12 --dry-run=client -o yaml \
#     | scripts/secrets.sh encrypt projects/authos/api/manifests/keystore.enc.yaml
#
# Decryption needs an age private key at ~/.config/sops/age/keys.txt (the SOPS
# default) or in SOPS_AGE_KEY_FILE. docs/runbook/secrets.md covers the keys,
# the backup drill and what to do if a key leaks.
set -euo pipefail

# Every file here was written by this version. 3.13.2 broke the MAC on files
# written by 3.13.1 (getsops/sops#2243), so a version change is made on
# purpose, never picked up by accident.
SOPS_VERSION=3.13.3

cd "$(git rev-parse --show-toplevel)"

usage() {
  sed -n '7,11p' "$0" | sed 's/^# *//' >&2
  exit 2
}

check_sops_version() {
  local installed
  installed="$(sops --disable-version-check --version | awk '{print $2}')"
  if [ "$installed" != "$SOPS_VERSION" ]; then
    echo "warning: sops $installed installed, this repo pins $SOPS_VERSION (scripts/secrets.sh)" >&2
  fi
}

require_enc_name() {
  [[ "$1" == *.enc.yaml ]] || { echo "error: $1 must end in .enc.yaml" >&2; exit 2; }
}

# Asks SOPS itself rather than guessing from the file's text.
is_encrypted() {
  sops filestatus --input-type yaml "$1" 2>/dev/null | grep -q '"encrypted":true'
}

enc_files() {
  local path
  for path in "$@"; do
    if [ -d "$path" ]; then
      find "$path" -name '*.enc.yaml' -type f | sort
    else
      require_enc_name "$path"
      echo "$path"
    fi
  done
}

# Pipes every decrypted manifest under the remaining arguments into "$1".
for_each_decrypted() {
  local action="$1" files f
  shift
  files="$(enc_files "$@")"
  [ -n "$files" ] || { echo "error: no .enc.yaml files under $*" >&2; exit 1; }
  while read -r f; do
    is_encrypted "$f" || { echo "error: $f is not encrypted, refusing" >&2; exit 1; }
    sops decrypt "$f" | "$action"
  done <<<"$files"
}

# Server-side: a client-side apply stores a second full copy of the Secret in
# the last-applied-configuration annotation.
kubectl_apply() {
  kubectl apply --server-side --field-manager=secrets-sh -f -
}

# kubectl diff masks Secret values, so its output is safe to show. Exit 1 means
# drift was found, which is the answer, not a failure.
kubectl_diff() {
  local rc=0
  kubectl diff --server-side --field-manager=secrets-sh -f - || rc=$?
  [ "$rc" -le 1 ]
}

cmd="${1:-}"
[ -n "$cmd" ] || usage
shift
check_sops_version

case "$cmd" in
  edit)
    [ $# -eq 1 ] || usage
    require_enc_name "$1"
    sops edit "$1"
    ;;
  encrypt)
    [ $# -eq 1 ] || usage
    require_enc_name "$1"
    if ! sops encrypt --filename-override "$1" --input-type yaml --output-type yaml /dev/stdin > "$1.tmp"; then
      rm -f "$1.tmp"
      exit 1
    fi
    mv "$1.tmp" "$1"
    echo "encrypted: $1"
    ;;
  apply)
    [ $# -ge 1 ] || usage
    for_each_decrypted kubectl_apply "$@"
    ;;
  diff)
    [ $# -ge 1 ] || usage
    for_each_decrypted kubectl_diff "$@"
    ;;
  check)
    # --staged reads the index, not the working tree: that is what a commit
    # would publish. The pre-commit hook in .githooks/ runs it that way.
    status=0
    if [ "${1:-}" = "--staged" ]; then
      while read -r f; do
        [ -n "$f" ] || continue
        is_encrypted <(git show ":$f") || { echo "NOT ENCRYPTED: $f" >&2; status=1; }
      done <<<"$(git diff --cached --name-only --diff-filter=ACMR -- '*.enc.yaml')"
    else
      while read -r f; do
        [ -n "$f" ] || continue
        is_encrypted "$f" || { echo "NOT ENCRYPTED: $f" >&2; status=1; }
      done <<<"$(git ls-files '*.enc.yaml')"
    fi
    exit "$status"
    ;;
  *)
    usage
    ;;
esac
