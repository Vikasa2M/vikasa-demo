# Build context is the PARENT of both repos (`GolandProjects`), not this repo
# root, because go.mod has `replace github.com/openits/openits-models =>
# ../../openits-models-pinned` and that sibling checkout must be present in
# the build context for the replace to resolve. Build with `make docker-build`
# (see Makefile), which does the `cd ../..` and passes this path with `-f`:
#
#   cd ../.. && docker build -f vikasa/vikasa-demo/Dockerfile -t vikasa-demo:dev .
#
# The context therefore looks like:
#   <context>/openits-models-pinned/...  (insulated clone pinned to 75f1fdb;
#                                          see docs/MODELS-PIN.md)
#   <context>/vikasa/vikasa-demo/...   (this repo)
# and is copied here preserving that layout so `../../openits-models-pinned`
# resolves exactly as it does on disk.
FROM golang:1.26-alpine AS build
ENV GOTOOLCHAIN=auto
WORKDIR /build/vikasa/vikasa-demo
COPY openits-models-pinned /build/openits-models-pinned
COPY vikasa/vikasa-demo /build/vikasa/vikasa-demo
RUN go build -o /out/ ./cmd/...

FROM alpine:3.20
COPY --from=build /out/ /usr/local/bin/
