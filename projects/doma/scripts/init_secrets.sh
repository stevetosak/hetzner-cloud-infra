#!/bin/bash
# Creates the `doma` namespace credentials Secret. Values are entered
# interactively — nothing lands in this file or in git.
#
# DATABASE_URL: doma's database lives on the shared authos-pg-cluster,
# owned by the existing `authos` role (same one wasteio/imaps use, not a
# new one). Look up its password with:
#   kubectl get secret db-credentials -n pg-cluster -o jsonpath='{.data.password}' | base64 -d
# then assemble:
#   postgresql://authos:<password>@authos-pg-cluster-rw.pg-cluster.svc.cluster.local:5432/doma
set -euo pipefail

read -rsp "DATABASE_URL: " DATABASE_URL
echo
read -rsp "SESSION_SECRET (openssl rand -hex 32): " SESSION_SECRET
echo
read -rsp "GOOGLE_CLIENT_SECRET: " GOOGLE_CLIENT_SECRET
echo
# Both optional — leave blank (just press enter) until the Telegram bot
# exists (@BotFather gives the token; the webhook secret is any random
# string you choose, e.g. `openssl rand -hex 32`). Chore reminders and
# account linking simply stay unavailable with these blank.
read -rsp "TELEGRAM_BOT_TOKEN (optional): " TELEGRAM_BOT_TOKEN
echo
read -rsp "TELEGRAM_WEBHOOK_SECRET (optional): " TELEGRAM_WEBHOOK_SECRET
echo

kubectl create secret generic credentials -n doma \
  --from-literal=DATABASE_URL="$DATABASE_URL" \
  --from-literal=SESSION_SECRET="$SESSION_SECRET" \
  --from-literal=GOOGLE_CLIENT_SECRET="$GOOGLE_CLIENT_SECRET" \
  --from-literal=TELEGRAM_BOT_TOKEN="$TELEGRAM_BOT_TOKEN" \
  --from-literal=TELEGRAM_WEBHOOK_SECRET="$TELEGRAM_WEBHOOK_SECRET"
