# Duster (authos BFF proxy)

Deployed like `authos-api` / `authos-ui`: CI (`authos` repo `.github/workflows/duster.yaml`)
builds `stevetosak/authos-duster:alpha-<sha>`, runs `kustomize edit set image` in
`manifests/overlays/dev`, and pushes. ArgoCD Application `authos-duster` (project `authos`, path
`projects/authos/duster/manifests/overlays/dev`) syncs the Deployment.

Runs in the **`authos`** namespace, shares the cluster Redis
(`redis-master.redis.svc.cluster.local:6379`) — all Duster keys are `duster:*`.
`AUTHOS_BASE_URL=https://authos-api.tosak.net` (byte-matches the discovery `issuer`).

## Applied out of band (not in the kustomize base / ArgoCD sync)

Same convention as `authos-api`: ArgoCD manages only `base/deployment.yaml` via the overlay.

```bash
kubectl apply -f manifests/configmap.yaml   # authos-duster-config
kubectl apply -f manifests/service.yaml     # duster (ClusterIP :8785)

# duster-admin: committed encrypted, applied with SOPS (ADR 0003). credentials lives under api/.
scripts/secrets.sh apply projects/authos/duster/manifests/duster-admin.enc.yaml
```

`manifests/duster-admin.yaml` documents the shape. A new token is `openssl rand -hex 32`,
written with `scripts/secrets.sh edit projects/authos/duster/manifests/duster-admin.enc.yaml`.

`REDIS_PASSWORD` is sourced from the existing `credentials` secret (`.REDIS_PASS`) by the
Deployment — no separate Redis secret for Duster.

The `DUSTER_ADMIN_TOKEN` must match whatever `dstr` / the demo bootstrap uses to call
`/duster/api/v1/internal/*`.
