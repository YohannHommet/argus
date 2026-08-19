# syntax=docker/dockerfile:1
#
# Multi-stage build for argusd (docs/SPEC.md §8.1). Build context is the
# REPO ROOT, not server/ — this Dockerfile copies from both server/ and
# web/, so it must be invoked as:
#
#   docker build -f server/Dockerfile \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     -t ghcr.io/yohannhommet/argus:latest .
#
# Deviation from the SPEC's literal single-line ENTRYPOINT: this Dockerfile
# uses ENTRYPOINT ["/argusd"] + CMD ["serve"] instead of
# ENTRYPOINT ["/argusd", "serve"]. `docker run <img>` still starts the
# server (CMD supplies "serve"), but `docker run <img> version` (or any
# other subcommand) now reaches argusd's own dispatcher instead of being
# appended after "serve" as an extra, ignored argument. Functionally
# equivalent for the default path, strictly more useful for the others.

# --- web: build the Vue SPA -------------------------------------------------
FROM node:26-alpine AS web
WORKDIR /web
# Pin pnpm explicitly: Node 25 no longer bundles corepack (it was removed
# from core, see nodejs/node#59802), and web/package.json has no
# "packageManager" field yet, so install the exact pnpm version directly.
RUN npm install -g pnpm@11.17.0
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# --- build: compile argusd, embedding the SPA build -------------------------
FROM golang:1.27rc3-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
# go.mod/go.sum first so `go mod download` is its own cached layer.
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
# Overlay the real SPA build over the committed placeholder — this happens
# only inside the build image; the checked-out source tree is untouched.
COPY --from=web /web/dist/. ./internal/httpapi/assets/dist/
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/YohannHommet/argus/server/internal/telemetry.Version=${VERSION} \
      -X github.com/YohannHommet/argus/server/internal/telemetry.Commit=${COMMIT}" \
    -o /out/argusd ./cmd/argusd

# --- final: distroless, nonroot, static ------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/argusd /argusd
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/argusd"]
CMD ["serve"]
