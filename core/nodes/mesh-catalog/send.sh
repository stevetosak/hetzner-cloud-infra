#!/usr/bin/env bash
# The CronJob `mesh-catalog` (cronjob.yaml): it sends the mesh store
# kube-system/mesh-peers to the workflow mesh-catalog.yml, but only when the
# store differs from peers.json on the public branch `mesh-catalog`. A
# dispatch that is lost leaves that difference, so the next run sends again.
#
# Exit 0: the catalog matches the store, or the store was sent.
# Exit 1: the store holds a field the catalog must not have (an endpoint, for
# example), the catalog cannot be read, or the dispatch failed. The failed Job
# raises KubeJobFailed.
set -euo pipefail

: "${STORE_NAMESPACE:=kube-system}"
: "${STORE_NAME:=mesh-peers}"
: "${REPOSITORY:=stevetosak/hetzner-cloud-infra}"
: "${CATALOG_URL:=https://raw.githubusercontent.com/$REPOSITORY/mesh-catalog/peers.json}"
: "${TOKEN_FILE:=/run/secrets/github/github-token}"
: "${WORK:=/tmp}"

store=$(kubectl -n "$STORE_NAMESPACE" get configmap "$STORE_NAME" -o json --ignore-not-found)
[ -n "$store" ] || store='{}' # no store yet: no machine has joined the mesh
peers=$(jq -c '(.data // {}) | map_values(fromjson)' <<<"$store")

# The workflow refuses these too; refusing here makes the Job fail, so the
# alert names the cluster side of the fault.
bad=$(jq -r 'to_entries[] | select((.value | type) != "object" or (.value | keys) != ["address", "publicKey", "site"]) | .key' <<<"$peers")
if [ -n "$bad" ]; then
  echo "$STORE_NAMESPACE/$STORE_NAME: these entries hold other fields than site, address and publicKey: $bad" >&2
  exit 1
fi

code=$(curl -sS --max-time 30 -o "$WORK/catalog.json" -w '%{http_code}' "$CATALOG_URL")
case "$code" in
  200) published=$(jq -c . "$WORK/catalog.json") ;;
  404) published='{}' ;; # no catalog yet: the branch or the file does not exist
  *) echo "reading $CATALOG_URL: HTTP $code" >&2; exit 1 ;;
esac

count=$(jq 'length' <<<"$peers")
if [ "$(jq -S -c . <<<"$peers")" = "$(jq -S -c . <<<"$published")" ]; then
  echo "the catalog matches the store ($count machines); nothing to send"
  exit 0
fi

if [ ! -s "$TOKEN_FILE" ]; then
  echo "the store differs from the catalog, but $TOKEN_FILE is empty or missing (Secret mesh-catalog-github)" >&2
  exit 1
fi

# The token goes to curl as a header file on a pipe, never as an argument.
jq -n --argjson peers "$peers" '{event_type: "mesh-peers", client_payload: {peers: $peers}}' |
  curl -sS --fail-with-body --max-time 30 -X POST \
    -H @<(printf 'Authorization: Bearer %s\n' "$(cat "$TOKEN_FILE")") \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2022-11-28' \
    --data-binary @- \
    "https://api.github.com/repos/$REPOSITORY/dispatches"
echo "sent the store ($count machines) to $REPOSITORY"
