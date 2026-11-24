# node-controller (ADR 0016, Amendment 2026-11-24). Build it from tools/kluster:
#
#   docker build -f cmd/node-controller/Dockerfile -t node-controller .
#
# CI (.github/workflows/node-controller.yml) builds it the same way and pushes
# it to ghcr.io/stevetosak/kluster-node-controller. One static binary on
# distroless: no shell, no package manager.
FROM golang:1.26.2@sha256:b54cbf583d390341599d7bcbc062425c081105cc5ef6d170ced98ef9d047c716 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/node-controller ./cmd/node-controller

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
LABEL org.opencontainers.image.source="https://github.com/stevetosak/hetzner-cloud-infra" \
      org.opencontainers.image.description="node-controller: removes a dead Worker of the tosak cluster"
COPY --from=build /out/node-controller /node-controller
USER 10001:10001
ENV PORT=8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD ["/node-controller", "-health-check"]
ENTRYPOINT ["/node-controller"]
