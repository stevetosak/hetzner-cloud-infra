# tosak-net site

The landing page at **https://tosak.net**; `www.tosak.net` redirects to it
with a 302. The image `stevetosak/tosak-net-site` is the static Astro build
behind unprivileged nginx on port 8080 (`apps/site/Dockerfile` in
[stevetosak/tosak-net](https://github.com/stevetosak/tosak-net)). It reads no
ConfigMap and no Secret; `/health/live` and `/health` are answered by nginx.

The ArgoCD Application `tosak-net-site` syncs the overlay. The tosak-net CI
pushes `latest` and `alpha-<sha>` from `main`; its deploy job bumps `newTag`
through the tag-bump action.

## Applied out of band

```bash
cd tools/kluster && kluster services seed --env live          # the namespace (Plan Mode first)
kubectl apply -f projects/tosak-net/site/manifests/service.yaml
kubectl apply -f projects/tosak-net/httproute.yaml
```
