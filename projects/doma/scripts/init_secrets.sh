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
# DATABASE_URL: doma's database lives on the shared tosak-pg-cluster,
# owned by the shared `tosak` role (same one wasteio/imaps use, not a
# new one). Look up its password with:
#   kubectl get secret db-credentials -n pg-cluster -o jsonpath='{.data.password}' | base64 -d
# then assemble:
#   postgresql://tosak:<password>@tosak-pg-cluster-rw.pg-cluster.svc.cluster.local:5432/doma
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
# THE TWO GO TOGETHER. Fill in both, or leave both blank. The TOKEN alone is
# what `isTelegramConfigured()` tests, so with it blank the bot never starts
# and `/api/telegram/webhook` answers 404 — safe, and chore reminders and
# account linking are simply unavailable.
#
# 🔴 THE WEBHOOK SECRET IS NOT OPTIONAL ONCE THE TOKEN IS SET. It is the only
# authentication on a PUBLIC POST endpoint: doma's HTTPRoute exposes everything
# under `/`, and grammy skips the `X-Telegram-Bot-Api-Secret-Token` check
# entirely when the value is empty (`secretToken: optionalEnv(...) || undefined`
# in src/core/notify/telegram-bot.ts). Anyone who finds the URL could then post
# forged Telegram updates, which is what drives account linking. Worse, boot
# calls `setWebhook(..., { secret_token: undefined })`, so a blank value also
# CLEARS any secret registered with Telegram earlier.
#
# The bot exists — `configmap.yaml` already names @domche_bot. Blank these only
# when the token is not to hand, and re-run this script later.
prompt_field TELEGRAM_BOT_TOKEN "TELEGRAM_BOT_TOKEN (blank only if not to hand)"
prompt_field TELEGRAM_WEBHOOK_SECRET "TELEGRAM_WEBHOOK_SECRET (openssl rand -hex 32; required if the token is set)"

kubectl create secret generic credentials -n doma \
  --from-literal=DATABASE_URL="$DATABASE_URL" \
  --from-literal=SESSION_SECRET="$SESSION_SECRET" \
  --from-literal=GOOGLE_CLIENT_SECRET="$GOOGLE_CLIENT_SECRET" \
  --from-literal=TELEGRAM_BOT_TOKEN="$TELEGRAM_BOT_TOKEN" \
  --from-literal=TELEGRAM_WEBHOOK_SECRET="$TELEGRAM_WEBHOOK_SECRET" \
  --dry-run=client -o yaml | kubectl apply -f -
