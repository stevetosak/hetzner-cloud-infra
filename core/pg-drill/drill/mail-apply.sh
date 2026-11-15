#!/bin/sh
# The drill Stalwart's first step (drill/mail-pod.yaml, init container
# apply-settings). The restored data store holds production's settings: the
# listeners on 25, 465 and 993, the relay route to SES and the S3 blob store.
# A normal start would open them. So Stalwart starts in recovery mode (only
# the HTTP listener on 8080), applies drill/mail-settings.ndjson, which
# replaces each of them, and stops again; the sidecar `stalwart` then starts
# normally on the drill's settings.
#
# The twin of projects/mail/stalwart/manifests/base/config/apply-settings.sh,
# without its placeholders: the drill plan holds no value from outside.
set -eu

PLAN=/drill/mail-settings.ndjson
CLI_SOURCE=/opt/stalwart-cli/usr/local/bin/stalwart-cli
CLI=/tmp/stalwart-cli
READY_URL=http://127.0.0.1:8080/healthz/ready
READY_TIMEOUT=120

fail() {
	echo "mail-apply: $*" >&2
	exit 1
}

# An image volume is mounted noexec: run a copy of the CLI from /tmp.
cp "$CLI_SOURCE" "$CLI"
chmod 0755 "$CLI"

STALWART_RECOVERY_MODE=1 /usr/local/bin/stalwart --config /etc/stalwart/config.json &
STALWART_PID=$!
trap 'kill -TERM "$STALWART_PID" 2>/dev/null || true; wait "$STALWART_PID" 2>/dev/null || true' EXIT

waited=0
until curl -fsS -o /dev/null "$READY_URL" 2>/dev/null; do
	kill -0 "$STALWART_PID" 2>/dev/null || fail "Stalwart stopped before it was ready"
	waited=$((waited + 1))
	[ "$waited" -lt "$READY_TIMEOUT" ] || fail "Stalwart was not ready after ${READY_TIMEOUT}s"
	sleep 1
done

STALWART_URL=http://127.0.0.1:8080 XDG_CACHE_HOME=/tmp/cache "$CLI" apply --file "$PLAN"
echo "mail-apply: the drill plan is applied"
