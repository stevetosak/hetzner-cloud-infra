# Deployment catalog

`history.jsonl` is an append-only record of every app version ArgoCD has reported
`Synced` + `Healthy` in the cluster. `CATALOG.md` is the readable roll-up, regenerated
from it. Both are written by `.github/workflows/deploy-catalog.yml`, never by hand.

## How a row is added

1. ArgoCD Notifications (`core/argocd/notifications/`) fires `on-deployed`, once per
   running image set (`oncePer: app.status.summary.images`), as a GitHub
   `repository_dispatch` (`event_type: deploy-live`).
2. `deploy-catalog.yml` clones the app repo, resolves the running `alpha-<sha>` image
   tag to a `git describe` semver, appends a line here, regenerates `CATALOG.md`, opens
   a GitHub Deployment on the app repo, and sends a Telegram message.

## Replay a missed event

Actions -> "Deploy catalog" -> Run workflow -> `payload`:

    { "app": "doma", "project": "default", "revision": "<infra sha>", "image": "stevetosak/doma:alpha-<app sha>" }

Appends are idempotent on `app`+`sha`. A replay that appends nothing is silent (no
commit, no Telegram); a replay for a genuinely new `app`+`sha` records and notifies.

## Adding an app

Fill its row in `apps.json`:

- `repo` — `owner/name` of the app repo, or `null` for "resolve nothing, record
  `version: unknown`, still notify, no GitHub Deployment".
- `tagPrefix` — the app's release-tag prefix (`git describe --match "<prefix>[0-9]*"`).
- `host` (optional) — the app's public host, used for the GitHub Deployment
  `environment_url`. Defaults to `<app>.tosak.net` when unset.

## Required infra-repo Actions secrets

Set under Settings -> Secrets and variables -> Actions on `stevetosak/hetzner-cloud-infra`:

- `APP_DEPLOY_PAT` — fine-grained PAT, permission **Deployments: Read and write**, scoped
  to the app repos (currently `stevetosak/doma`). Used to open the GitHub Deployment +
  status on the app repo. If unset, that step warns and is skipped.
- `INFRA_TELEGRAM_BOT_TOKEN` — bot token for the notification message.
- `INFRA_TELEGRAM_CHAT_ID` — target chat id. If either Telegram secret is unset, the
  Telegram step warns and is skipped.

(`APP_DEPLOY_PAT` is a different token from the ArgoCD notifications controller's
`github-token`, which needs **Contents: Read and write** on the infra repo to POST the
`repository_dispatch` — see `core/argocd/notifications/README.md`.)
