# third_party/vikasa-infra — vendored topology renderer

This is a **partial, vendored copy** of the topology renderer from
[`github.com/Vikasa2M/vikasa-infra`](https://github.com/Vikasa2M/vikasa-infra),
copied in so a fresh clone of `vikasa-demo` can render its NATS/stream
configs (`make topology`) with **no sibling checkout** next to the repo.

- **Source commit:** `3464f82ae6f14a2db3825ae036d561eae1af8a08`
- **What's included:** `cmd/gen` (the renderer entry point) and the
  `internal/` packages it depends on, plus a self-contained `vendor/` so it
  builds fully offline. Tests and testdata were stripped — this copy exists
  only to *build and run* `cmd/gen`, not to develop it.
- **It is its own Go module** (`github.com/Vikasa2M/vikasa-infra`), nested
  under `vikasa-demo`. The parent module's `go build ./...` / `go test ./...`
  skip it automatically; it is only ever invoked as `cd third_party/vikasa-infra
  && go run ./cmd/gen …` by the Makefile's `topology` target.

## Temporary — replace with a real dependency once released

This vendored copy is a stopgap until `vikasa-infra` publishes a consumable
GitHub release. When it does, delete this directory and either `go get` the
released module or point `INFRA_DIR` back at a pinned checkout — see the
`topology` target and `INFRA_DIR` in the `Makefile`.

## Refreshing this copy

If the renderer changes upstream and this copy needs to move forward before
the release lands, from a `vikasa-infra` checkout at the new commit:

```sh
SRC=/path/to/vikasa-infra
DEST=third_party/vikasa-infra
rm -rf $DEST && mkdir -p $DEST/cmd/gen
cp $SRC/go.mod $SRC/go.sum $DEST/
cp $SRC/cmd/gen/main.go $DEST/cmd/gen/
cp -R $SRC/internal $DEST/
cd $DEST
find . -name '*_test.go' -delete
find . -type d -name testdata -exec rm -rf {} +
go mod tidy && go mod vendor
GOPROXY=off GOFLAGS=-mod=vendor go build ./cmd/gen   # must build offline
```

Then confirm it still renders byte-identical output to upstream before
committing (render both into temp dirs and `diff -rq`), and update the source
commit above.
