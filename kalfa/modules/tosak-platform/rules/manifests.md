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
          resources:
            requests:
              cpu: 200m
              memory: 512Mi
            limits:
              cpu: 500m
              memory: 1Gi
      volumes:
        - name: tmp
          emptyDir: {}
  strategy:
    type: Recreate
```

A lightweight internal service can use smaller resources (for example requests `100m`/`256Mi`, limits `500m`/`512Mi`).

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
