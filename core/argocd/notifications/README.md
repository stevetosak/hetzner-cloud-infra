# ArgoCD Notifications — deploy-live

Emits a GitHub `repository_dispatch` (`event_type: deploy-live`) to
`stevetosak/hetzner-cloud-infra` when any Application goes `Synced` + `Healthy` on a
new `sync.revision`. `.github/workflows/deploy-catalog.yml` consumes it. Global
`subscriptions`, no per-Application annotations — a future ApplicationSet migration
changes nothing here.

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

## The PAT

Fine-grained, single repo `stevetosak/hetzner-cloud-infra`, permission **Contents:
Read and write**. Separate from the infra repo's `APP_DEPLOY_PAT` Actions secret
(**Deployments: write** on the app repos).
