#!/bin/bash
# The Restore Drill (ADR 0003, Amendments 2026-10-01 and 2026-10-02; runbook
# step 17). Run monthly by cronjob.yaml.
#
# Restores the production backups into a throw-away one-instance Cluster in
# `pg-drill`, asserts, reports, and deletes the Cluster:
#
#   1. the restored Cluster is Ready within READY_TIMEOUT (30m);
#   2. the replay reached the newest WAL segment in the bucket, listed BEFORE
#      the restore — physical replay is exact, so reaching it means the rows
#      are production's rows;
#   3. every database production declares exists, with at least one user
#      table and more than zero rows.
#
# Pass or fail, it writes drill/<UTC timestamp>.json to tosak-drill-results
# (read by .github/scripts/watch.mjs `checkDrill`) and sends one Telegram
# message.
#
# 🔴 Never print a credential, a row or an API body. Names, counts and LSNs only.
set -euo pipefail

: "${READY_TIMEOUT:=30m}"
: "${JOB_NAME:=restore-drill}"

DRILL_NS=pg-drill
DRILL=drill                       # the Cluster in drill/cluster.yaml
DRILL_POD=$DRILL-1                # its one instance
STORE=tosak-pg-backups-readonly   # objectstore.yaml
PROD_NS=pg-cluster
PROD=tosak-pg-cluster
PLUGIN=barman-cloud.cloudnative-pg.io
RESULTS_BUCKET=tosak-drill-results
TEMPLATE=/drill/cluster.yaml

step="start"            # what was running when it failed
result=fail
server="" newest_wal="" lsn_reached="" ready_seconds=""
db_report=()            # "authos: 9 tables, 1234 rows"
db_json='[]'

log() { printf '%s  %s\n' "$(date -u +%H:%M:%S)" "$*"; }

# Two R2 tokens; each command names the one it uses.
aws_readonly() {
  AWS_ACCESS_KEY_ID=$RO_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$RO_ACCESS_SECRET_KEY \
    aws --endpoint-url "$ENDPOINT" "$@"
}
aws_results() {
  AWS_ACCESS_KEY_ID=$RESULTS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$RESULTS_ACCESS_SECRET_KEY \
    aws --endpoint-url "$ENDPOINT" "$@"
}

# psql as `postgres` over the instance's local socket: no credential needed.
psql_drill() {
  kubectl exec -i -n "$DRILL_NS" "$DRILL_POD" -c postgres -- \
    psql -X -v ON_ERROR_STOP=1 -tA -F ' ' "$@"
}

# Deletes the drill Cluster and waits until its PVC is gone. CloudNativePG
# owns the PVC; its PV is `Delete`, so the Hetzner volume goes with it.
delete_drill_cluster() {
  kubectl delete cluster "$DRILL" -n "$DRILL_NS" --ignore-not-found --wait --timeout=5m
  local _
  for _ in $(seq 60); do
    [ -z "$(kubectl get pvc -n "$DRILL_NS" -l "cnpg.io/cluster=$DRILL" -o name)" ] && return 0
    sleep 5
  done
  echo "the drill PVC is still there after 5 minutes" >&2
  return 1
}

# The bot token goes to curl on stdin, not argv.
telegram() {
  curl -sS -o /dev/null -w '%{http_code}' --max-time 30 -K - \
    --data-urlencode "chat_id=$TG_CHAT_ID" --data-urlencode "text=$1" \
    <<<"url = \"https://api.telegram.org/bot$TG_BOT_TOKEN/sendMessage\""
}

finish() {
  local code=$? cleaned=yes key written=yes text http finished_at
  trap - EXIT
  set +e
  [ "$result" = pass ] || log "FAILED at: $step (exit $code)"

  log "deleting the drill Cluster"
  delete_drill_cluster || cleaned=no

  finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  key="drill/$(date -u +%Y%m%dT%H%M%SZ).json"
  jq -n --arg finishedAt "$finished_at" --arg result "$result" \
        --arg failedAt "$([ "$result" = pass ] || echo "$step")" \
        --arg serverName "$server" --arg newestWal "$newest_wal" \
        --arg lsnReached "$lsn_reached" --arg readySeconds "$ready_seconds" \
        --arg cleanedUp "$cleaned" --argjson databases "$db_json" \
        '{finishedAt: $finishedAt, result: $result, failedAt: (if $failedAt == "" then null else $failedAt end),
          serverName: $serverName, newestWal: $newestWal, lsnReached: $lsnReached,
          readySeconds: ($readySeconds | tonumber? // null), databases: $databases,
          cleanedUp: ($cleanedUp == "yes")}' >/tmp/result.json
  if [ -z "${ENDPOINT:-}" ] \
     || ! aws_results s3 cp /tmp/result.json "s3://$RESULTS_BUCKET/$key" --quiet; then
    written=no
  fi
  log "result: $result, written to $RESULTS_BUCKET/$key: $written, drill Cluster deleted: $cleaned"

  if [ "$result" = pass ]; then
    text="✅ Restore Drill passed — $finished_at
Restored $server to LSN $lsn_reached (newest archived segment $newest_wal), Ready in ${ready_seconds}s.
$(printf '%s\n' "${db_report[@]}")"
  else
    text="🔴 Restore Drill FAILED — $finished_at
Failed at: $step.
Log: kubectl logs -n $DRILL_NS job/$JOB_NAME"
  fi
  [ "$written" = yes ] || text+=$'\n'"⚠️ The result was NOT written to $RESULTS_BUCKET — the watcher will report a missing pass."
  [ "$cleaned" = yes ] || text+=$'\n'"⚠️ The drill Cluster or its volume was NOT deleted. The next run deletes it; check: kubectl get cluster,pvc -n $DRILL_NS"

  http=$(telegram "$text")
  log "telegram: HTTP $http"

  [ "$result" = pass ] && [ "$written" = yes ] && [ "$http" = 200 ] && exit 0
  exit 1
}
trap finish EXIT
# The Job's deadline sends SIGTERM; exit through `finish` so the Cluster is
# still deleted and the failure still reported.
trap 'step+=" (killed by the Job deadline)"; exit 143' TERM

step="delete a drill Cluster left by an earlier run"
log "$step"
delete_drill_cluster

step="read what production declares"
log "$step"
prod=$(kubectl get cluster "$PROD" -n "$PROD_NS" -o json)
image=$(jq -er '.spec.imageName' <<<"$prod")
server=$(jq -er --arg p "$PLUGIN" '.spec.plugins[] | select(.name == $p) | .parameters.serverName' <<<"$prod")
# Fetched into a variable first: a failure inside `< <(…)` would be silent,
# and the drill would check fewer databases and still pass.
prod_dbs=$(kubectl get databases.postgresql.cnpg.io -n "$PROD_NS" -o json)
mapfile -t databases < <(
  {
    jq -r '.spec.bootstrap.initdb.database // empty' <<<"$prod"
    jq -r --arg c "$PROD" '.items[]
      | select(.spec.cluster.name == $c and (.spec.ensure // "present") == "present")
      | .spec.name' <<<"$prod_dbs"
  } | sort -u
)
[ "${#databases[@]}" -gt 0 ] || { echo "production declares no database" >&2; exit 1; }
log "image $image, archive $server, databases: ${databases[*]}"

step="read the bucket from the drill's ObjectStore"
store=$(kubectl get objectstores.barmancloud.cnpg.io "$STORE" -n "$DRILL_NS" -o json)
ENDPOINT=$(jq -er '.spec.configuration.endpointURL' <<<"$store")
bucket=$(jq -er '.spec.configuration.destinationPath' <<<"$store")
bucket=${bucket#s3://}; bucket=${bucket%%/*}

# Segment names sort in WAL order — timeline first, then position. Skip
# `.history`, `.partial` and `.backup` objects; allow a compression suffix.
step="find the newest archived WAL segment"
log "$step in s3://$bucket/$server/wals/"
newest_wal=$(
  aws_readonly s3api list-objects-v2 --bucket "$bucket" --prefix "$server/wals/" \
    --query 'Contents[].Key' --output text |
    tr '\t' '\n' | sed 's|.*/||' |
    grep -E '^[0-9A-F]{24}(\.(zst|gz|bz2|xz|lz4|snappy))?$' | cut -c1-24 | sort | tail -1
)
log "newest archived segment: $newest_wal"

step="create the drill Cluster"
log "$step"
yq -o=json "$TEMPLATE" |
  jq --arg image "$image" --arg server "$server" \
    '.spec.imageName = $image
     | .spec.externalClusters[0].plugin.parameters.serverName = $server' |
  kubectl create -f -
created_epoch=$(date -u +%s)

step="wait for the restore to be Ready (limit $READY_TIMEOUT)"
log "$step"
# In the background, so a SIGTERM reaches the trap without waiting for it.
kubectl wait cluster/"$DRILL" -n "$DRILL_NS" --for=condition=Ready --timeout="$READY_TIMEOUT" &
wait $!
ready_seconds=$(($(date -u +%s) - created_epoch))
log "Ready after ${ready_seconds}s"

# After the recovery the instance starts normally, so pg_last_wal_replay_lsn()
# is NULL there. The end of the replay is recorded instead as the switch point
# of the new timeline, the last line of its .history file. pg_walfile_name()
# maps an LSN exactly on a segment boundary to the segment before it, so the
# comparison is right for both ways a segment can end.
step="check the replay reached segment $newest_wal"
log "$step"
read -r lsn_reached reached < <(psql_drill -v newest="$newest_wal" <<'SQL'
WITH history AS (
  SELECT pg_read_file(format('pg_wal/%s.history', lpad(upper(to_hex(timeline_id)), 8, '0'))) AS body
  FROM pg_control_checkpoint()
), switch AS (
  SELECT (regexp_match(body, E'([0-9A-F]+/[0-9A-F]+)\\t[^\\n]*\\n?$'))[1]::pg_lsn AS lsn
  FROM history
)
SELECT lsn, substr(pg_walfile_name(lsn), 9) >= substr(:'newest', 9) FROM switch;
SQL
)
log "replay ended at $lsn_reached, reached the newest segment: $reached"
[ "$reached" = t ]

step="check every declared database has data"
log "$step"
missing=()
for db in "${databases[@]}"; do
  if ! counts=$(psql_drill -d "$db" <<'SQL'
SELECT count(*),
       coalesce(sum((xpath('/row/n/text()',
         query_to_xml(format('SELECT count(*) AS n FROM %I.%I', schemaname, tablename),
                      false, true, '')))[1]::text::bigint), 0)
FROM pg_tables
WHERE schemaname NOT IN ('pg_catalog', 'information_schema');
SQL
  ); then
    missing+=("$db: cannot be read")
    db_json=$(jq --arg n "$db" '. + [{name: $n, ok: false}]' <<<"$db_json")
    continue
  fi
  read -r tables rows <<<"$counts"
  db_report+=("$db: $tables tables, $rows rows")
  ok=false
  if [ "$tables" -ge 1 ] && [ "$rows" -gt 0 ]; then ok=true; else missing+=("$db: $tables tables, $rows rows"); fi
  db_json=$(jq --arg n "$db" --argjson t "$tables" --argjson r "$rows" --argjson ok "$ok" \
    '. + [{name: $n, tables: $t, rows: $r, ok: $ok}]' <<<"$db_json")
  log "$db: $tables tables, $rows rows"
done
if [ "${#missing[@]}" -gt 0 ]; then
  step="database check: ${missing[*]}"
  exit 1
fi

result=pass
log "all three assertions passed"
