#!/bin/sh
set -eu
# NOTE: the compose `migrate` service mounts ../clickhouse:/migrations:ro, so
# the numbered migration files (which live in deploy/clickhouse/migrations/
# per the task layout) appear at /migrations/migrations/*.sql inside the
# container, while this script and seed.generated.sql (top level of
# deploy/clickhouse/, gitignored — rendered from deploy/topology/fleet.yaml
# by `make gen-compose`, see tools/gen-compose) appear directly at
# /migrations/*.
CH="clickhouse-client --host ${CH_HOST:-clickhouse}"
$CH --queries-file /migrations/migrations/001_databases.sql
for db in vikasa_mardot vikasa_veldot vikasa_sabdot vikasa_federation; do
  for f in /migrations/migrations/002_event_tables.sql /migrations/migrations/003_dimensions.sql \
           /migrations/migrations/004_rollups.sql /migrations/migrations/005_dead_letter.sql; do
    sed "s/__DB__/$db/g" "$f" | $CH -n
  done
done
[ -f /migrations/seed.generated.sql ] && $CH -n < /migrations/seed.generated.sql
# seed.generated.sql re-inserts every row on every run; cabinets/devices are
# ReplacingMergeTree and only dedup on background merge, so a bare count()
# right after a re-run would double-count until that merge happens. Force
# it immediately so the migrate service is idempotent from the caller's
# point of view (this runs every `make demo-slice`, not just once).
for db in vikasa_mardot vikasa_veldot vikasa_sabdot vikasa_federation; do
  $CH -q "OPTIMIZE TABLE $db.cabinets FINAL"
  $CH -q "OPTIMIZE TABLE $db.devices FINAL"
done
echo migrations applied
