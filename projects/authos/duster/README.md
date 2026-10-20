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

# duster-admin: committed encrypted (ADR 0003). credentials lives under api/.
# From tools/kluster:
go run . secrets apply --apply authos/duster-admin
```

`secrets.yaml` at the top describes the Secret. A new token is
`go run . secrets rotate --apply authos/duster-admin.DUSTER_ADMIN_TOKEN`; dstr and
authos-demo must then get the same value.

`REDIS_PASSWORD` is sourced from the existing `credentials` secret (`.REDIS_PASS`) by the
Deployment — no separate Redis secret for Duster.

The `DUSTER_ADMIN_TOKEN` must match whatever `dstr` / the demo bootstrap uses to call
`/duster/api/v1/internal/*`.
