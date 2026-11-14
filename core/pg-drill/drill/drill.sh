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
#      table and more than zero rows;
#   4. the mail phase (ADR 0003, Amendment 2026-11-02), only when production
#      declares the database `mail` and 1–3 passed: the mail bucket is copied
#      as it was at the replay end, a throw-away Stalwart (drill/mail-pod.yaml)
#      starts on the restored `mail` database and that copy, and
#      drill/mail_check.py reads the canary and SAMPLE random messages back.
#
# Pass or fail, it writes drill/<UTC timestamp>.json to tosak-drill-results
# (read by .github/scripts/watch.mjs `checkDrill`) and sends one Telegram
# message. With REPORT=log (the rehearsal cut core/rehearsal/pg-drill) it does
# neither and only logs the result object, so a rehearsal pass never reaches
# the watcher or the chat.
#
# 🔴 Never print a credential, a row or an API body. Names, counts and LSNs only.
set -euo pipefail

: "${READY_TIMEOUT:=30m}"
: "${JOB_NAME:=restore-drill}"
: "${MAIL_BUCKET:=tosak-mail-blobs}"
: "${CANARY_SHA256:=}"
: "${MAIL_TIMEOUT:=2100}"           # seconds: the pod's own deadline is 1800
: "${REPORT:=all}"                  # all: results bucket and Telegram; log: the log only
case $REPORT in
  all|log) ;;
  *) echo "REPORT=$REPORT: want all or log" >&2; exit 2 ;;
esac

DRILL_NS=pg-drill
DRILL=drill                       # the Cluster in drill/cluster.yaml
DRILL_POD=$DRILL-1                # its one instance
STORE=tosak-pg-backups-readonly   # objectstore.yaml
PROD_NS=pg-cluster
PROD=tosak-pg-cluster
PLUGIN=barman-cloud.cloudnative-pg.io
RESULTS_BUCKET=tosak-drill-results
TEMPLATE=/drill/cluster.yaml
MAIL_DB=mail
MAIL_POD=drill-stalwart             # drill/mail-pod.yaml
MAIL_SECRET=drill-stalwart          # this run's passwords; deleted by `finish`
MAIL_TEMPLATE=/drill/mail-pod.yaml
MAIL_ROLE=tosak                     # the owner of `mail`, as in production
PROD_MAIL_NS=mail
PROD_MAIL_DEPLOY=stalwart           # gives the Stalwart and stalwart-cli images
S3_HOST=s3.nl-ams.scw.cloud

step="start"            # what was running when it failed
result=fail
server="" newest_wal="" lsn_reached="" ready_seconds=""
owns_cluster=no         # set once no other drill is running; until then
                        # `finish` must not delete a Cluster `drill`
db_report=()            # "authos: 9 tables, 1234 rows"
db_json='[]'
mail_json='{"result":"skipped"}'

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
#
# Explicit returns: it is also called as `delete_drill_cluster || …`, where
# errexit is off. A failed `get` is never read as "gone".
delete_drill_cluster() {
  kubectl delete cluster "$DRILL" -n "$DRILL_NS" --ignore-not-found --wait --timeout=5m || return 1
  local _ left
  for _ in $(seq 60); do
    if left=$(kubectl get pvc -n "$DRILL_NS" -l "cnpg.io/cluster=$DRILL" -o name); then
      [ -z "$left" ] && return 0
    fi
    sleep 5
  done
  echo "the drill PVC is still there after 5 minutes" >&2
  return 1
}

# Deletes this run's drill Stalwart and its Secret. Explicit returns, as above.
delete_drill_mail() {
  kubectl delete pod "$MAIL_POD" -n "$DRILL_NS" --ignore-not-found --wait --timeout=5m || return 1
  kubectl delete secret "$MAIL_SECRET" -n "$DRILL_NS" --ignore-not-found || return 1
}

# The bot token goes to curl on stdin, not argv.
telegram() {
  curl -sS -o /dev/null -w '%{http_code}' --max-time 30 -K - \
    --data-urlencode "chat_id=$TG_CHAT_ID" --data-urlencode "text=$1" \
    <<<"url = \"https://api.telegram.org/bot$TG_BOT_TOKEN/sendMessage\""
}

# One line for the Telegram message on a pass.
mail_summary() {
  jq -r 'if .result == "skipped" then "mail: skipped (production declares no mail database)"
         else "mail: canary \(.canary), \(.sampled) of \(.messages) messages read back, \(.blobFiles) files (\(.blobBytes / 1048576 | floor) MiB) as of \(.versionAt)" end' \
    <<<"$mail_json" 2>/dev/null || echo "mail: no result"
}

finish() {
  local code=$? cleaned=skipped key written=yes text http finished_at
  trap - EXIT
  set +e
  [ "$result" = pass ] || log "FAILED at: $step (exit $code)"

  if [ "$owns_cluster" = yes ]; then
    log "deleting the drill Cluster"
    cleaned=yes
    delete_drill_mail || cleaned=no
    delete_drill_cluster || cleaned=no
  fi

  finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  key="drill/$(date -u +%Y%m%dT%H%M%SZ).json"
  jq -n --arg finishedAt "$finished_at" --arg result "$result" \
        --arg failedAt "$([ "$result" = pass ] || echo "$step")" \
        --arg serverName "$server" --arg newestWal "$newest_wal" \
        --arg lsnReached "$lsn_reached" --arg readySeconds "$ready_seconds" \
        --arg cleanedUp "$cleaned" --argjson databases "$db_json" \
        --argjson mail "$(jq -c . <<<"$mail_json" 2>/dev/null || echo '{"result":"fail"}')" \
        '{finishedAt: $finishedAt, result: $result, failedAt: (if $failedAt == "" then null else $failedAt end),
          serverName: $serverName, newestWal: $newestWal, lsnReached: $lsnReached,
          readySeconds: ($readySeconds | tonumber? // null), databases: $databases, mail: $mail,
          cleanedUp: ($cleanedUp == "yes")}' >/tmp/result.json
  # Never upload an empty or broken object: the watcher cannot parse it and
  # would report itself broken every day until someone deleted it.
  # (`-s` too: jq 1.6 exits 0 on empty input.)
  if ! { [ -s /tmp/result.json ] && jq -e '.finishedAt and .result' /tmp/result.json >/dev/null 2>&1; }; then
    printf '{"finishedAt":"%s","result":"%s"}\n' "$finished_at" "$result" >/tmp/result.json
  fi
  if [ "$REPORT" = log ]; then
    log "result: $result (REPORT=log: nothing written to $RESULTS_BUCKET, no Telegram message), drill Cluster deleted: $cleaned"
    log "result object: $(jq -c . /tmp/result.json)"
    [ "$result" = pass ] && exit 0
    exit 1
  fi
  if [ -z "${ENDPOINT:-}" ] \
     || ! aws_results s3 cp /tmp/result.json "s3://$RESULTS_BUCKET/$key" --quiet; then
    written=no
  fi
  log "result: $result, written to $RESULTS_BUCKET/$key: $written, drill Cluster deleted: $cleaned"

  if [ "$result" = pass ]; then
    text="✅ Restore Drill passed — $finished_at
Restored $server to LSN $lsn_reached (newest archived segment $newest_wal), Ready in ${ready_seconds}s.
$(printf '%s\n' "${db_report[@]}")
$(mail_summary)"
  else
    text="🔴 Restore Drill FAILED — $finished_at
Failed at: $step.
Log: kubectl logs -n $DRILL_NS job/$JOB_NAME"
  fi
  [ "$written" = yes ] || text+=$'\n'"⚠️ The result was NOT written to $RESULTS_BUCKET — the watcher will report a missing pass."
  [ "$cleaned" != no ] || text+=$'\n'"⚠️ The drill Cluster, its volume or the drill Stalwart was NOT deleted. The next run deletes it; check: kubectl get cluster,pvc,pod,secret -n $DRILL_NS"

  http=$(telegram "$text")
  log "telegram: HTTP $http"

  [ "$result" = pass ] && [ "$written" = yes ] && [ "$http" = 200 ] && exit 0
  exit 1
}
# The mail phase (ADR 0003, Amendment 2026-11-02). Runs after the database
# check, so the restored `mail` database is known to be readable.
mail_phase() {
  local version_at logged ended epoch deploy image cli_image configmap pg_ip s3_ip pod_json phase line failed
  local deadline=$((SECONDS + MAIL_TIMEOUT))

  # PostgreSQL logs the time of the last replayed commit when the replay ends.
  # The bucket is copied as of the next whole second: a file written before
  # that commit is in the copy, one written after it at most adds an unused
  # file. Only a UTC time is accepted, so the conversion cannot shift it.
  step="mail: read the replay end time from the restore's log"
  log "$step"
  logged=$(
    for pod in $(kubectl get pods -n "$DRILL_NS" -l "cnpg.io/cluster=$DRILL" -o name); do
      kubectl logs -n "$DRILL_NS" "$pod" --all-containers 2>/dev/null || true
    done | jq -Rr 'fromjson? | .record.message? // empty' |
      sed -n 's/^last completed transaction was at log time //p' | tail -1
  )
  ended=$(grep -Eo '^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?\+00$' <<<"$logged" | cut -c1-19 || true)
  if [ -z "$ended" ]; then
    step="mail: the restore logged no UTC replay end time (got '${logged:-nothing}')"
    exit 1
  fi
  epoch=$(date -u -d "$ended" +%s)
  version_at=$(date -u -d "@$((epoch + 1))" +%Y-%m-%dT%H:%M:%SZ)
  log "last replayed commit $logged; the bucket is copied as of $version_at"
  mail_json=$(jq -n --arg at "$version_at" '{result: "fail", versionAt: $at}')

  step="mail: read production's Stalwart images"
  deploy=$(kubectl get deployment "$PROD_MAIL_DEPLOY" -n "$PROD_MAIL_NS" -o json)
  image=$(jq -er '.spec.template.spec.containers[] | select(.name == "stalwart") | .image' <<<"$deploy")
  cli_image=$(jq -er '.spec.template.spec.volumes[] | select(.name == "stalwart-cli") | .image.reference' <<<"$deploy")
  configmap=$(kubectl get pod "$HOSTNAME" -n "$DRILL_NS" -o json |
    jq -er '.spec.volumes[] | select(.name == "drill") | .configMap.name')
  pg_ip=$(kubectl get service "$DRILL-rw" -n "$DRILL_NS" -o jsonpath='{.spec.clusterIP}')
  s3_ip=$(getent ahostsv4 "$S3_HOST" | awk 'NR == 1 { print $1 }')
  # One test per line: under errexit, a failed test before `&&` does not stop.
  [ -n "$pg_ip" ] || { step="mail: the drill Cluster has no $DRILL-rw address"; exit 1; }
  [ -n "$s3_ip" ] || { step="mail: $S3_HOST does not resolve"; exit 1; }
  log "Stalwart $image, drill database $pg_ip, $S3_HOST $s3_ip"

  # Fresh for every run and never printed: the drill Stalwart's admin, and
  # the password of the owner role in the throw-away copy only. Production's
  # passwords never reach the drill.
  step="mail: set this run's passwords"
  mkdir -p /tmp/mail
  od -An -tx1 -N24 /dev/urandom | tr -d ' \n' >/tmp/mail/admin-password
  od -An -tx1 -N24 /dev/urandom | tr -d ' \n' >/tmp/mail/db-password
  [ "$(wc -c </tmp/mail/admin-password)" -eq 48 ]
  [ "$(wc -c </tmp/mail/db-password)" -eq 48 ]
  # On stdin, so the value is in no argument list.
  printf "ALTER ROLE %s PASSWORD '%s';\n" "$MAIL_ROLE" "$(cat /tmp/mail/db-password)" | psql_drill -d postgres -q
  kubectl create secret generic "$MAIL_SECRET" -n "$DRILL_NS" \
    --from-file=admin-password=/tmp/mail/admin-password --from-file=db-password=/tmp/mail/db-password >/dev/null
  rm -f /tmp/mail/admin-password /tmp/mail/db-password

  step="mail: start the drill Stalwart"
  log "$step"
  pod_json=$(yq -o=json 'explode(.)' "$MAIL_TEMPLATE" |
    jq --arg pg "$pg_ip" --arg s3ip "$s3_ip" --arg s3host "$S3_HOST" --arg bucket "$MAIL_BUCKET" \
       --arg at "$version_at" --arg image "$image" --arg cli "$cli_image" --arg cm "$configmap" \
       --arg canary "$CANARY_SHA256" '
      def setenv($n; $v): .env |= map(if .name == $n then .value = $v else . end);
      .spec.hostAliases = [{ip: $pg, hostnames: ["drill-rw"]}, {ip: $s3ip, hostnames: [$s3host]}]
      | .spec.initContainers |= map(
          if .name == "copy-blobs" then setenv("MAIL_BUCKET"; $bucket) | setenv("RCLONE_S3_VERSION_AT"; $at)
          elif .name == "apply-settings" or .name == "stalwart" then .image = $image
          else . end)
      | .spec.containers |= map(if .name == "check" then setenv("CANARY_SHA256"; $canary) else . end)
      | .spec.volumes |= map(
          if .name == "drill" then .configMap.name = $cm
          elif .name == "stalwart-cli" then .image.reference = $cli
          else . end)')
  if grep -q 'set-by-drill.sh' <<<"$pod_json"; then
    step="mail: drill/mail-pod.yaml still holds a set-by-drill.sh value"
    exit 1
  fi
  kubectl create -f - <<<"$pod_json" >/dev/null

  step="mail: wait for the read-back (limit ${MAIL_TIMEOUT}s)"
  log "$step"
  while :; do
    phase=$(kubectl get pod "$MAIL_POD" -n "$DRILL_NS" -o jsonpath='{.status.phase}')
    case $phase in Succeeded | Failed) break ;; esac
    [ "$SECONDS" -lt "$deadline" ] || exit 1
    sleep 10
  done
  log "the drill Stalwart pod ended: $phase"

  step="mail: read the result"
  line=$(kubectl logs "$MAIL_POD" -n "$DRILL_NS" -c check --tail=1 2>/dev/null || true)
  if jq -e '.result' <<<"$line" >/dev/null 2>&1; then
    mail_json=$(jq -c --arg at "$version_at" '. + {versionAt: $at}' <<<"$line")
  else
    # The pod ended before `check` wrote a result: name the container that
    # failed, in start order (the sidecar last: it is killed when the pod
    # ends), and show the end of its log. Those containers log no message and
    # no address; `check` is never shown.
    failed=$(kubectl get pod "$MAIL_POD" -n "$DRILL_NS" -o json | jq -r '
      def bad: (.state.terminated.exitCode // 0) != 0;
      (.status.reason // "") as $reason
      | ([(.status.initContainerStatuses // [])[] | select(.name != "stalwart" and bad) | .name]
         + [(.status.containerStatuses // [])[] | select(bad) | .name]
         + [(.status.initContainerStatuses // [])[] | select(.name == "stalwart" and (bad or .state.waiting != null)) | .name]
         | first // "no container") + (if $reason == "" then "" else " (\($reason))" end)')
    case $failed in
      copy-blobs* | apply-settings* | stalwart*)
        kubectl logs "$MAIL_POD" -n "$DRILL_NS" -c "${failed%% *}" --tail=20 2>/dev/null | sed 's/^/    /' || true ;;
    esac
    mail_json=$(jq -nc --arg at "$version_at" --arg c "$failed" \
      '{result: "fail", versionAt: $at, failures: ["the pod ended in \($c) before a result"]}')
  fi
  log "mail: $(jq -c '{result, canary, accounts, messages, sampled, blobFiles, blobBytes}' <<<"$mail_json")"
  if [ "$(jq -r .result <<<"$mail_json")" != pass ]; then
    step="mail: $(jq -r '.failures // [] | join("; ")' <<<"$mail_json")"
    exit 1
  fi
}

trap finish EXIT
# The Job's deadline sends SIGTERM; exit through `finish` so the Cluster is
# still deleted and the failure still reported.
trap 'step+=" (killed by the Job deadline)"; exit 143' TERM

# First, so that a failure in any later step can still write its result.
step="read the bucket from the drill's ObjectStore"
store=$(kubectl get objectstores.barmancloud.cnpg.io "$STORE" -n "$DRILL_NS" -o json)
ENDPOINT=$(jq -er '.spec.configuration.endpointURL' <<<"$store")
bucket=$(jq -er '.spec.configuration.destinationPath' <<<"$store")
bucket=${bucket#s3://}; bucket=${bucket%%/*}

# Every run uses the Cluster name `drill`. concurrencyPolicy: Forbid does not
# cover a Job made by hand, and a second run would delete the first one's
# restore — and the first one's `kubectl wait` could then pass on the
# second one's Cluster.
step="check that no other drill is running"
running=$(kubectl get pods -n "$DRILL_NS" -l batch.kubernetes.io/job-name \
  --field-selector=status.phase=Running \
  -o jsonpath='{range .items[*]}{.metadata.labels.batch\.kubernetes\.io/job-name}{"\n"}{end}')
others=$(grep -vx -e "$JOB_NAME" -e '' <<<"$running" || true)
if [ -n "$others" ]; then
  step="refused to start: another drill is running ($(tr '\n' ' ' <<<"$others"))"
  exit 1
fi
owns_cluster=yes

step="delete a drill Cluster and Stalwart left by an earlier run"
log "$step"
delete_drill_mail
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

if printf '%s\n' "${databases[@]}" | grep -qx "$MAIL_DB"; then
  mail_phase
else
  log "production declares no '$MAIL_DB' database: the mail phase is skipped"
fi

result=pass
log "every assertion passed"
