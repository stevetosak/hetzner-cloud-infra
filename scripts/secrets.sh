#!/bin/bash
# The one way Secrets reach the cluster (ADR 0003). Every Secret is a whole
# Kubernetes manifest, `<name>.enc.yaml`, committed beside its blank template
# `<name>.yaml` and encrypted with SOPS + age. Only `data` and `stringData` are
# encrypted (.sops.yaml), so key names stay readable in review.
#
#   scripts/secrets.sh edit <file.enc.yaml>        open in $EDITOR, create if new
#   scripts/secrets.sh encrypt <file.enc.yaml>     encrypt a Secret manifest read on stdin
#   scripts/secrets.sh apply <file|dir>...         decrypt and kubectl apply
#   scripts/secrets.sh check [--staged]            fail on any .enc.yaml that is not encrypted
#
# `encrypt` is for values that already live in a file or in the cluster, so the
# plaintext never touches disk:
#
#   kubectl create secret generic keystore -n authos \
#     --from-file=keystore.p12=/etc/keystore/authos-cluster-keystore.p12 --dry-run=client -o yaml \
#     | scripts/secrets.sh encrypt projects/authos/api/manifests/keystore.enc.yaml
#
# Decryption needs the age private key at ~/.config/sops/age/keys.txt (the SOPS
# default) or in SOPS_AGE_KEY_FILE. It lives in the password manager, never in
# the cluster or the repo.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

usage() {
  sed -n '8,11p' "$0" | sed 's/^# *//' >&2
  exit 2
}

require_enc_name() {
  [[ "$1" == *.enc.yaml ]] || { echo "error: $1 must end in .enc.yaml" >&2; exit 2; }
}

# A file is encrypted when SOPS wrote its metadata block.
is_encrypted() {
  grep -q '^sops:' "$1"
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

cmd="${1:-}"
[ -n "$cmd" ] || usage
shift

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
    files="$(enc_files "$@")"
    [ -n "$files" ] || { echo "error: no .enc.yaml files under $*" >&2; exit 1; }
    while read -r f; do
      is_encrypted "$f" || { echo "error: $f is not encrypted, refusing" >&2; exit 1; }
      sops decrypt "$f" | kubectl apply -f -
    done <<<"$files"
    ;;
  check)
    # --staged reads the index, not the working tree: that is what a commit
    # would publish. The pre-commit hook in .githooks/ runs it that way.
    status=0
    if [ "${1:-}" = "--staged" ]; then
      files="$(git diff --cached --name-only --diff-filter=ACMR -- '*.enc.yaml')"
      while read -r f; do
        [ -n "$f" ] || continue
        grep -q "^sops:" <(git show ":$f") || { echo "NOT ENCRYPTED: $f" >&2; status=1; }
      done <<<"$files"
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
