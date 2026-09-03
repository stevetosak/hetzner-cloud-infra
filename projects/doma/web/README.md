# doma (household hub)

Deployed at **https://doma.tosak.net**. TanStack Start (Node/Nitro) server, own database on
the shared `authos-pg-cluster` (owned by the existing `authos` role — same as wasteio/imaps,
not a new role).

CI (`doma` repo `.github/workflows/deploy.yaml`) gates on typecheck/lint/format/unit tests,
builds `stevetosak/doma:alpha-<sha>`, runs `kustomize edit set image` in
`manifests/overlays/dev`, and pushes. ArgoCD Application `doma` (project `default`, path
`projects/doma/web/manifests/overlays/dev`) syncs the Deployment.

Migrations run at container boot (`scripts/start.mjs` in the app repo), behind a Postgres
advisory lock — safe with `replicas: 1` / `strategy: Recreate`.

**Env-var contract note:** the app takes a single `DATABASE_URL` connection string and
`APP_ORIGIN`, not the discrete `DB_HOST`/`DB_PORT`/`DB_NAME`/`PUBLIC_ORIGIN` vars an earlier
draft of the deploy plan assumed — `configmap.yaml` / `credentials.yaml` here match what the
app actually reads (`src/core/env.ts`'s callers), not the draft.

## Applied out of band (not in the kustomize base / ArgoCD sync)

Same convention as `authos-api` / `duster`: ArgoCD manages only `base/deployment.yaml` via the
overlay. The rest is applied by hand.

```bash
# 1. Database CRD (once, from the repo root).
kubectl apply -f core/cnpg/databases/doma.yaml

# 2. Namespace + secrets (prompts interactively; nothing lands in git).
kubectl create namespace doma
bash projects/doma/scripts/init_secrets.sh

# 3. Fill in the real Google OAuth client id (from the Google Cloud Console step — it's
#    public, not a secret, but doma needs the real value to build its redirect URL).
$EDITOR projects/doma/web/manifests/configmap.yaml   # GOOGLE_CLIENT_ID: REPLACE_ME -> real value

# 4. Apply the out-of-band manifests.
kubectl apply -f projects/doma/web/manifests/configmap.yaml
kubectl apply -f projects/doma/web/manifests/service.yaml
kubectl apply -f projects/doma/ingress.yaml

# 5. After this overlay path is on infra master and deploy.yaml has bumped the image tag past
#    `latest`:
kubectl apply -f projects/doma/web/argocd-application.yaml
```

The `doma` repo also needs the variable **`INFRA_REPO_DOMA_OVERLAY_DIR`** =
`projects/doma/web/manifests/overlays/dev`, plus `DOCKERHUB_USERNAME` / `INFRA_REPO_NAME` and
the secrets `DOCKERHUB_TOKEN` / `INFRA_REPO_TOKEN` (same names every other project's deploy
workflow in this org uses).

`TELEGRAM_BOT_TOKEN` / `TELEGRAM_WEBHOOK_SECRET` aren't here yet — nothing in the app reads
them until M8 (the Telegram bot). Add them to `credentials.yaml` / `init_secrets.sh` when that
milestone actually ships.
