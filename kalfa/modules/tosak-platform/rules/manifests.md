---
paths:
  - "projects/**"
  - "**/manifests/**"
---
# Kubernetes manifests on the tosak platform

One component is `projects/<project>/<component>/` in cloud-infra:

```
README.md
manifests/
  configmap.yaml          # non-sensitive env vars (applied out of band)
  service.yaml            # ClusterIP, port 80 → the named container port `http` (out of band)
  credentials.enc.yaml    # the real Secret, SOPS-encrypted, listed in secrets.yaml
  base/
    deployment.yaml       # namespace-agnostic, no image tag, no replicas
    kustomization.yaml    # references only deployment.yaml
  overlays/dev/
    kustomization.yaml    # namespace, image tag (newTag), replica count
```

## `manifests/base/deployment.yaml`

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: <app>
  labels:
    app: <app>
spec:
  revisionHistoryLimit: 3
  selector:
    matchLabels:
      app: <app>
  template:
    metadata:
      labels:
        app: <app>
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        runAsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: <app>
          image: stevetosak/<image>
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: <port>
          envFrom:
            - configMapRef:
                name: <app>-config
            - secretRef:
                name: credentials
          securityContext:
            readOnlyRootFilesystem: true
            capabilities:
              drop: ['ALL']
          volumeMounts:
            - name: tmp
              mountPath: /tmp
          livenessProbe:
            httpGet:
              path: /health/live      # process only, no dependency checks
              port: http
            initialDelaySeconds: 5
            periodSeconds: 30
          readinessProbe:
            httpGet:
              path: /health           # checks the dependencies (DB, Redis)
              port: http
            initialDelaySeconds: 3
            periodSeconds: 10
          resources:             # a first guess; resized by the sizing rule below
            requests:
              cpu: 50m
              memory: 256Mi
            limits:
              memory: 512Mi       # 2x the request; no CPU limit
      volumes:
        - name: tmp
          emptyDir: {}
  strategy:
    type: Recreate
```

## Resources: the sizing rule

Requests follow measured peaks, not a house default (cloud-infra ADR 0013):

- Memory request: the peak working set times 1.3, rounded up to 16 Mi, never under 32 Mi. Memory limit: twice the request.
- CPU request: the peak 5-minute rate times 1.3, never under `10m`. No CPU limit on the cluster's own workloads; a client namespace gets one through its LimitRange.
- The peak comes from Prometheus over at least 3 days: `python3 -I core/monitoring/capacity-snapshot.py 3d`.
- A new component starts from a guess (the template's `50m` / `256Mi`, limit `512Mi`) and is resized once 3 days of data exist.
- After a resize, check the new pod's peak over its first hour against the new request; raise the request if the peak is higher.
- A process that sizes itself from a cap keeps the limit its cap needs: Redis `maxmemory` stays below the limit, and a JVM takes its heap from the limit. Only its request follows the rule.
- A periodic workload (a CronJob) keeps its values until a measured window holds one of its runs.
- Floors: PostgreSQL at least 384 Mi, Envoy at least 128 Mi.

## `manifests/base/kustomization.yaml`

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
```

## `manifests/overlays/dev/kustomization.yaml`

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: <namespace>
resources:
  - ../../base
images:
  - name: stevetosak/<image>
    newTag: latest              # CI bumps this to alpha-<sha>
replicas:
  - count: 1
    name: <app>
```

## The route: `projects/<project>/httproute.yaml`

One per project, shared by its components, applied out of band.

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: <project>
  namespace: <namespace>
spec:
  parentRefs:
    - name: tosak
      namespace: gateway
      sectionName: https
  hostnames:
    - <project>.tosak.net
  rules:
    - backendRefs:
        - name: <app>
          port: 80
```

- No `tls` block and no cert-manager annotation. One wildcard certificate for `*.tosak.net` lives in the `gateway` namespace, because a listener's certificate must be a Secret in the Gateway's own namespace.
- The Gateway's `allowedRoutes` in `core/gateway/gateway.yaml` must name the namespace, on both listeners, or the route is refused with `NotAllowedByListeners`. This is deliberate: under Ingress any namespace could claim any host.
- No `URLRewrite` filter unless a path really has to change. The Gateway API passes the path through unchanged, which an app that serves a sub-path (`/duster/api/v1/...`) needs.
- Envoy's default route timeout is 15 seconds. Server-sent events or any long-lived response need `timeouts: {request: 0s}` on the rule, or the stream is cut.
