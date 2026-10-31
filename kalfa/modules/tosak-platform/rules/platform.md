# The tosak platform

Every project of this workspace that deploys runs on one personal Kubernetes cluster on Hetzner Cloud (1 control plane, 3 workers). The infrastructure is the repository `stevetosak/hetzner-cloud-infra` (checked out as `cloud-infra/`).

## Cluster services

| Service | Namespace | Internal address | Notes |
|---|---|---|---|
| PostgreSQL (CloudNativePG) | `pg-cluster` | `tosak-pg-cluster-rw.pg-cluster.svc.cluster.local:5432` | Shared cluster; the role `tosak` owns every database |
| Redis | `redis` | `redis-master.redis.svc.cluster.local:6379` | Shared, password-protected. The same password is in the `redis` Secret and in `credentials`/`REDIS_PASS` in `authos`: rotate both together |
| Public entry point | `gateway` | — | Gateway `tosak`, class `tosak`; Hetzner LB, PROXY protocol, one wildcard certificate |
| Envoy Gateway | `envoy-gateway-system` | — | Implements the Gateway API. ingress-nginx is retired and gone |
| cert-manager | `cert-manager` | — | ClusterIssuer `letsencrypt-prod`, DNS-01 through Cloudflare |
| Storage | — | — | StorageClass `hcloud-volumes` (default). Longhorn was removed |
| Restore drill | `pg-drill` | — | Monthly CronJob: restores the PostgreSQL backups from R2 into a throw-away Cluster and asserts |
| Argo CD | `argocd` | — | `argocd.tosak.net`, auto-sync. Two ApplicationSets (`projects`, `core`) generate every Application; never write one by hand |

There is no private ingress class: a VPN-only host gets built again when something needs one.

## DNS

Only one subdomain level on `tosak.net` (Cloudflare free-plan TLS covers `*.tosak.net` only): `<project>[-<service>].tosak.net`, joined by a hyphen, for example `doma.tosak.net`, `authos-api.tosak.net`. Paths are fine: `docs.tosak.net/authos`. Never `x.<project>.tosak.net`.

## The pipeline

```
push to main → CI gate (tests, lint, typecheck) → Docker image → Docker Hub
  → tag-bump action (GitHub App tosak-tag-bump) → PR deploy/<app>-<sha7> on cloud-infra
    → deploy-scope check → auto-merge to the infra master
      → Argo CD syncs the overlay → the pod rolls → on-deployed trigger
        → repository_dispatch to cloud-infra → deploy catalog, Telegram, GitHub Deployment
```

Nobody pushes to the infra `master`: rulesets require a PR, and `deploy-scope` refuses an App PR that changes anything but one listed overlay's `newTag`.

## Conventions every app follows

- Image `stevetosak/<project>[-<component>]` on Docker Hub. Tags: `latest` (always pushed; the first overlay uses it) and `alpha-<full-sha>` (every CI build; the overlay's `newTag`).
- `/health/live` checks only the process (always 200 while it runs) and is the liveness probe; a dependency failure there restarts the pod, so it never checks the database or Redis. `/health` checks the dependencies and is the readiness probe; a failure takes the pod out of the Service without a restart.

## The parts that load with their files

These rules load when you read or write a matching file. Before you CREATE such a file, read its rule first (under Claude Code: `.claude/rules/kalfa-tosak-platform-<rule>.md`), because a new file gets its rule only after the first write.

| Rule | Files | What it holds |
|---|---|---|
| `docker` | `Dockerfile*`, `.dockerignore` | The image rules |
| `app-ci` | `.github/workflows/` | The app repository's build and deploy job, version strings |
| `manifests` | `projects/`, `manifests/` | The Deployment, Kustomize and HTTPRoute templates, the security and resource defaults |
| `add-project` | `core/`, `projects/`, `secrets.yaml`, `deployments/` | The cloud-infra layout, what Argo CD syncs, how to add a project, its database, Secrets and deploy-catalog entry, the first apply order |
| `hetzner` | `infra/`, `*.tf` | The Hetzner Cloud firewall limits and the `hcloud` provider fault |
