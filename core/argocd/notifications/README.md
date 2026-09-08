# ArgoCD Notifications — deploy-live

Emits a GitHub `repository_dispatch` (`event_type: deploy-live`) to
`stevetosak/hetzner-cloud-infra` when an Application goes `Synced` + `Healthy` with a
running image set it has not reported before. `.github/workflows/deploy-catalog.yml`
consumes it. Global `subscriptions`, no per-Application annotations — a future
ApplicationSet migration changes nothing here.

The trigger keys `oncePer` on `app.status.summary.images`, not `app.status.sync.revision`:
all 8 Applications track the infra repo unscoped, so `sync.revision` advances for every
app on every infra commit and would fire this trigger 8x per deploy (into a workflow
that no longer serializes). Keying on the image set fires once, for the app that
actually changed.

## Apply (run by the cluster owner — Claude's classifier blocks `kubectl apply`)

    # 1. Config (safe to re-apply; this file is the whole desired data).
    kubectl -n argocd apply -f core/argocd/notifications/argocd-notifications-cm.yaml

    # 2. Secret — patch in just the one key. Do NOT apply the .example file.
    kubectl -n argocd patch secret argocd-notifications-secret --type merge \
      -p "{\"stringData\":{\"github-token\":\"<FINE_GRAINED_PAT>\"}}"

    # 3. Reload (the controller also re-reads within ~60s on its own):
    kubectl -n argocd rollout restart deploy/argocd-notifications-controller

## Verify

    # Render the webhook body for a live app without sending it (binary name may be
    # `argocd-notifications` on PATH inside the pod — check `which`):
    kubectl -n argocd exec deploy/argocd-notifications-controller -- \
      argocd-notifications template notify app-deployed doma --recipient gh-infra

    kubectl -n argocd logs deploy/argocd-notifications-controller -f

Before trusting this in production, make a no-op infra commit (e.g. touch a comment,
push to `master`) and confirm the controller log shows **ONE** `on-deployed` fire, not
eight — one per Application. If you see eight, `oncePer` is not deduping and the
`deploy-catalog` workflow will be hammered; re-check the `oncePer` key in
`argocd-notifications-cm.yaml`.

## The PAT

Fine-grained, single repo `stevetosak/hetzner-cloud-infra`, permission **Contents:
Read and write**. Separate from the infra repo's `APP_DEPLOY_PAT` Actions secret
(**Deployments: write** on the app repos).
