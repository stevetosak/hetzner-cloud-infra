# authos-demo (The Handshake Trace)

The public guided walkthrough at **https://authos-demo.tosak.net** — a real tier-0 Authos +
Duster OIDC login drawn as a sequence diagram. Standalone React SPA served by nginx.

Deployed like `authos-api` / `authos-ui` / `duster`: CI (`authos` repo
`.github/workflows/demo.yaml`) gates on the e2e stack suite, builds
`stevetosak/authos-demo:alpha-<sha>`, runs `kustomize edit set image` in
`manifests/overlays/dev`, and pushes. ArgoCD Application `authos-demo` (project `authos`, path
`projects/authos/demo/manifests/overlays/dev`) syncs the Deployment.

Runs in the **`authos`** namespace, alongside Duster — the Ingress path-routes `/duster` to the
in-cluster `duster` Service, so the SPA and Duster share an origin (tier 0: no CORS,
`SameSite=Lax` cookie).

## Applied out of band (not in the kustomize base / ArgoCD sync)

Same convention as `authos-api` / `duster`: ArgoCD manages only `base/deployment.yaml` via the
overlay. The rest is applied by hand.

```bash
# 1. Provision the Duster-wired OAuth app and get its client_id (run once).
#    From the authos repo, authos-demo/:
kubectl port-forward -n authos svc/duster 8785:8785 &
DEMO_OWNER_EMAIL=demo-owner@…  DEMO_OWNER_PASSWORD=…  \
DEMO_DUSTER_ADMIN_TOKEN="$(kubectl get secret duster-admin -n authos -o jsonpath='{.data.DUSTER_ADMIN_TOKEN}' | base64 -d)" \
DEMO_DUSTER=http://localhost:8785 \
npm run bootstrap        # prints DEMO_DUSTER_CLIENT_ID

# 2. Put that client_id in the ConfigMap, then apply the out-of-band manifests.
$EDITOR manifests/configmap.yaml      # DEMO_DUSTER_CLIENT_ID: REPLACE_ME -> the printed value
kubectl apply -f manifests/configmap.yaml   # authos-demo-config
kubectl apply -f manifests/service.yaml     # authos-demo (ClusterIP :80)
kubectl apply -f manifests/ingress.yaml     # authos-demo.tosak.net (+ cert-manager TLS)

# 3. After this overlay path is on infra master and demo.yaml has bumped the image tag:
kubectl apply -f argocd-application.yaml
```

Set the printed `client_id` as the `authos` repo variable **`DEMO_DUSTER_CLIENT_ID`** as well —
it is not consumed by CI today, but keeps the source of truth next to the other deploy vars.

The `authos` repo also needs the variable **`INFRA_REPO_DEMO_OVERLAY_DIR`** =
`projects/authos/demo/manifests/overlays/dev` and the secrets `DEMO_OWNER_EMAIL` /
`DEMO_OWNER_PASSWORD` (for a future `workflow_dispatch` re-provision job; `bootstrap.ts` is run
by hand today).
