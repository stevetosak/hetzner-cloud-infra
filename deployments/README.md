# Deployment catalog

`history.jsonl` is an append-only record of every app version ArgoCD has reported
`Synced` + `Healthy` in the cluster. `CATALOG.md` is the readable roll-up, regenerated
from it. Both live on the branch `deploy-catalog` (not on `master`) and are written by
`.github/workflows/deploy-catalog.yml`, never by hand. The scripts and `apps.json` stay
on `master`.

This file is the operations guide. For the end-to-end architecture — every component,
the design rationale, and failure modes — see [`HOW-IT-WORKS.md`](HOW-IT-WORKS.md).

## How a row is added

1. ArgoCD Notifications (configured in `core/argocd/argocd-values.yaml`) fires
   `on-deployed`, once per
   running image set (`oncePer: app.status.summary.images`), as a GitHub
   `repository_dispatch` (`event_type: deploy-live`).
2. `deploy-catalog.yml` clones the app repo, resolves the running `alpha-<sha>` image
   tag to a `git describe` semver, appends a line to `history.jsonl` on `deploy-catalog`, regenerates `CATALOG.md`, opens
   a GitHub Deployment on the app repo, and sends a Telegram message.

## Replay a missed event

Actions -> "Deploy catalog" -> Run workflow -> `payload`:

    { "app": "doma", "project": "default", "revision": "<infra sha>", "image": "stevetosak/doma:alpha-<app sha>" }

Appends are idempotent on `app`+`sha`. A replay that appends nothing is silent (no
commit, no Telegram); a replay for a genuinely new `app`+`sha` records and notifies.

## Adding an app

Fill its row in `apps.json`:

- `repo` — `owner/name` of the app repo (all four Authos apps use `stevetosak/authos`), or `null` for "resolve nothing, record
  `version: unknown`, still notify, no GitHub Deployment".
- `tagPrefix` — the app's release-tag prefix (`git describe --match "<prefix>[0-9]*"`).
- `host` (optional) — the app's public host, used for the GitHub Deployment
  `environment_url`. Defaults to `<app>.tosak.net` when unset.

## Required infra-repo Actions secrets

Set under Settings -> Secrets and variables -> Actions on `stevetosak/hetzner-cloud-infra`:

- `DEPLOY_STATUS_APP_KEY` — the private key of the GitHub App `tosak-deploy-status`
  (Deployments: write, installed on `stevetosak/doma` and `stevetosak/authos`). The
  workflow mints a token for the one app repo to open the GitHub Deployment + status.
  The App's Client ID is in the workflow file (it is not a secret). If the key is
  missing, the run fails.
- `INFRA_TELEGRAM_BOT_TOKEN` — bot token for the notification message.
- `INFRA_TELEGRAM_CHAT_ID` — target chat id. If either Telegram secret is unset, the
  Telegram step warns and is skipped.

(The App key is a different credential from the ArgoCD notifications controller's
`github-token`, which needs **Contents: Read and write** on the infra repo to POST the
`repository_dispatch` — see `core/argocd/credentials.yaml`.)
