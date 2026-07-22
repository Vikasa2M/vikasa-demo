# Self-contained build: the context is this repo root (`make docker-build`
# runs `docker build -f Dockerfile -t vikasa-demo:dev .`). The image builds in
# vendor mode against the committed vendor/ tree, which already contains the
# pinned openits-models packages (`go.mod` has
# `replace github.com/openits/openits-models => ../../openits-models-pinned`,
# but -mod=vendor resolves everything from vendor/ and never consults that
# path). No sibling checkout needs to be present in the build context. See
# docs/MODELS-PIN.md.
FROM golang:1.26-alpine AS build
ENV GOTOOLCHAIN=auto
WORKDIR /build
COPY . .
RUN go build -mod=vendor -o /out/ ./cmd/...

FROM alpine:3.20
COPY --from=build /out/ /usr/local/bin/
