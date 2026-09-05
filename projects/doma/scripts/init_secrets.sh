#!/bin/bash
# Creates or updates the `doma` namespace credentials Secret. Values are
# entered interactively — nothing lands in this file or in git.
#
# Safe to re-run: every field is re-applied on each run (this rewrites the
# whole Secret), so leaving a prompt blank keeps that field's CURRENT value
# (fetched from the live secret) instead of blanking it out. That's what
# lets you re-run this later just to fill in the Telegram fields without
# retyping DATABASE_URL/SESSION_SECRET/GOOGLE_CLIENT_SECRET. To actually
# clear a field back to empty, use `kubectl edit secret credentials -n doma`
# instead of this script.
#
# DATABASE_URL: doma's database lives on the shared authos-pg-cluster,
# owned by the existing `authos` role (same one wasteio/imaps use, not a
# new one). Look up its password with:
#   kubectl get secret db-credentials -n pg-cluster -o jsonpath='{.data.password}' | base64 -d
# then assemble:
#   postgresql://authos:<password>@authos-pg-cluster-rw.pg-cluster.svc.cluster.local:5432/doma
set -euo pipefail

current_value() {
  kubectl get secret credentials -n doma -o jsonpath="{.data.$1}" 2>/dev/null | base64 -d 2>/dev/null || true
}

prompt_field() {
  local var_name="$1" label="$2" current suffix input
  current="$(current_value "$var_name")"
  suffix=""
  [ -n "$current" ] && suffix=" [enter keeps current value]"
  read -rsp "$label$suffix: " input
  echo
  if [ -z "$input" ] && [ -n "$current" ]; then
    printf -v "$var_name" '%s' "$current"
  else
    printf -v "$var_name" '%s' "$input"
  fi
}

prompt_field DATABASE_URL "DATABASE_URL"
prompt_field SESSION_SECRET "SESSION_SECRET (openssl rand -hex 32)"
prompt_field GOOGLE_CLIENT_SECRET "GOOGLE_CLIENT_SECRET"
# Both optional — leave blank (just press enter) until the Telegram bot
# exists (@BotFather gives the token; the webhook secret is any random
# string you choose, e.g. `openssl rand -hex 32`). Chore reminders and
# account linking simply stay unavailable with these blank.
prompt_field TELEGRAM_BOT_TOKEN "TELEGRAM_BOT_TOKEN (optional)"
prompt_field TELEGRAM_WEBHOOK_SECRET "TELEGRAM_WEBHOOK_SECRET (optional)"

kubectl create secret generic credentials -n doma \
  --from-literal=DATABASE_URL="$DATABASE_URL" \
  --from-literal=SESSION_SECRET="$SESSION_SECRET" \
  --from-literal=GOOGLE_CLIENT_SECRET="$GOOGLE_CLIENT_SECRET" \
  --from-literal=TELEGRAM_BOT_TOKEN="$TELEGRAM_BOT_TOKEN" \
  --from-literal=TELEGRAM_WEBHOOK_SECRET="$TELEGRAM_WEBHOOK_SECRET" \
  --dry-run=client -o yaml | kubectl apply -f -
