---
paths:
  - "**/Dockerfile*"
  - "**/*.Dockerfile"
  - "**/.dockerignore"
---
# Docker images on the tosak platform

- Push to Docker Hub as `stevetosak/<project>[-<component>]`.
- Pin a specific base image, an LTS runtime (for example `eclipse-temurin:21-jre`, `node:22-slim`).
- Run as a non-root numeric UID: `USER 10001`. The pod runs with `runAsUser: 10001` and `readOnlyRootFilesystem: true`, with `/tmp` as the only writable path.
- Include a `.dockerignore`.
- Accept `PORT` as an environment variable where it applies.
- Add a `HEALTHCHECK` on a lightweight endpoint (`/health/live`).
- An app with release-please gets its version as the build argument `APP_VERSION` (see the `app-ci` rule).
