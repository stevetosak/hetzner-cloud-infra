#!/bin/sh
# Applies the settings plan from git before Stalwart's first normal start.
# Runs as an init container from the Stalwart image: it starts Stalwart in
# recovery mode (only the HTTP listener on 8080), applies the plan with
# stalwart-cli and stops Stalwart again. The main container then starts
# normally and finds every setting in the data store.
set -eu

PLAN_TEMPLATE=/etc/stalwart/settings.ndjson
PLAN=/tmp/settings.ndjson
CLI_SOURCE=/opt/stalwart-cli/usr/local/bin/stalwart-cli
CLI=/tmp/stalwart-cli
READY_URL=http://127.0.0.1:8080/healthz/ready
READY_TIMEOUT=120

fail() {
	echo "apply-settings: $*" >&2
	exit 1
}

# A value goes into the plan only from this allowlist, and only when it is a
# plain name: no quote, backslash or other character can change the JSON.
check_plain() {
	case $2 in
		'' | *[!A-Za-z0-9._-]*) fail "$1 is empty or holds a character outside [A-Za-z0-9._-]" ;;
	esac
}

: "${S3_BUCKET:?S3_BUCKET is not set}"
SES_SMTP_USERNAME=$(cat /run/secrets/ses-smtp/username)
check_plain S3_BUCKET "$S3_BUCKET"
check_plain "the ses-smtp username" "$SES_SMTP_USERNAME"

sed -e "s/\${S3_BUCKET}/$S3_BUCKET/g" \
	-e "s/\${SES_SMTP_USERNAME}/$SES_SMTP_USERNAME/g" \
	"$PLAN_TEMPLATE" >"$PLAN"
if grep -q '\${' "$PLAN"; then
	fail "the plan still holds a \${...} placeholder"
fi

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

# The CLI keeps a schema cache under XDG_CACHE_HOME; the root file system is
# read-only.
STALWART_URL=http://127.0.0.1:8080 XDG_CACHE_HOME=/tmp/cache "$CLI" apply --file "$PLAN"
echo "apply-settings: the plan is applied"
