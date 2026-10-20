# Reloader

Restarts the readers of a changed Secret, whatever changed it: kluster, a hand
apply, or an ArgoCD sync ([ADR 0003](../../docs/adr/0003-recoverability.md),
Amendment 2026-10-06). Stakater Reloader, a pinned helm render, applied by hand.

```
./core/reloader/render.sh                         # after a values change
kubectl apply -f core/reloader/namespace.yaml
kubectl apply -f core/reloader/reloader.yaml
```

- **Opt-in.** A Deployment carries `secret.reloader.stakater.com/auto: "true"`
  in its base `deployment.yaml`. Today: `redis`, `authos-api`, `duster`, `doma`.
  A new Secret reader adds the annotation.
- **Scoped, no ClusterRole.** `reloader-values.yaml` lists the watched
  namespaces (`redis`, `authos`, `doma`). A new project that reads a Secret adds
  its namespace there and renders again, or Reloader never sees its Secret.
- **Annotation strategy.** A restart writes the pod-template annotation
  `reloader.stakater.com/last-reloaded-from`. The ApplicationSet ignores it, so
  ArgoCD does not report drift.
- **Not covered:** CloudNativePG reads `pg-cluster/db-credentials` itself (the
  `cnpg.io/reload` label), and cert-manager and ArgoCD read their Secrets on
  demand.
- **Check it:** change a Secret, then
  `kubectl -n <ns> get deploy <name> -o jsonpath='{.spec.template.metadata.annotations}'`
  shows the annotation, and the pod is new.
