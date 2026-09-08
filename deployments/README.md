# Deployment catalog

`history.jsonl` is an append-only record of every app version ArgoCD has reported
`Synced` + `Healthy` in the cluster. `CATALOG.md` is the readable roll-up, regenerated
from it. Both are written by `.github/workflows/deploy-catalog.yml`, never by hand.

## How a row is added

1. ArgoCD Notifications (`core/argocd/notifications/`) fires `on-deployed`, once per
   `sync.revision`, as a GitHub `repository_dispatch` (`event_type: deploy-live`).
2. `deploy-catalog.yml` clones the app repo, resolves the running `alpha-<sha>` image
   tag to a `git describe` semver, appends a line here, regenerates `CATALOG.md`, opens
   a GitHub Deployment on the app repo, and sends a Telegram message.

## Replay a missed event

Actions -> "Deploy catalog" -> Run workflow -> `payload`:

    { "app": "doma", "project": "default", "revision": "<infra sha>", "image": "stevetosak/doma:alpha-<app sha>" }

Appends are idempotent on `app`+`sha`, so a replay is safe (it will not double-commit;
it will still re-send Telegram).

## Adding an app

Fill its row in `apps.json` (`repo`, `tagPrefix`). `repo: null` means "resolve nothing,
record `version: unknown`, still notify, no GitHub Deployment".
