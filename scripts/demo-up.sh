#!/bin/sh
# Batched bring-up for the ~100-cabinet demo fleet.
#
# A single `docker compose up -d --wait` over the whole fleet (~210 cabinet
# containers plus infra) overwhelms the Docker daemon's container-create path:
# observed live at 99 cabinets, the daemon disconnects mid-bring-up (the CLI
# loses the socket; containers still come up, but stream-init and the sinks
# never run, so no data flows). RAM is not the constraint — steady state is
# ~6 GB — the spike is purely the simultaneous create/network-attach storm.
#
# This script paces the bring-up: infra and the NATS tiers first, then the
# cabinet leaves and sims in small batches, each `--wait`ed before the next so
# container creation never bursts. Same end state as the old inline `make demo`
# recipe, just reliable at scale. Tunable via BATCH (default 20).
set -eu

CF="-f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.generated.yml"
BATCH=${BATCH:-20}

dc() { docker compose $CF "$@"; }

# up_batched SERVICES LABEL — bring SERVICES (whitespace-separated) up BATCH at
# a time, waiting for each batch before starting the next.
up_batched() {
	_label="$2"
	# shellcheck disable=SC2086  # deliberate word-split of the service list
	set -- $1
	_total=$#
	_done=0
	while [ $# -gt 0 ]; do
		_batch=""
		_n=0
		while [ $# -gt 0 ] && [ "$_n" -lt "$BATCH" ]; do
			_batch="$_batch $1"
			shift
			_n=$((_n + 1))
		done
		_done=$((_done + _n))
		echo ">> $_label $_done/$_total"
		# shellcheck disable=SC2086
		dc up -d --wait $_batch
	done
}

echo ">> shared infra (clickhouse / grafana / prometheus)"
dc up -d --wait clickhouse grafana prometheus

echo ">> migrate (schema + dimension seed)"
dc run --rm migrate

echo ">> NATS tiers + tier exporters"
# shellcheck disable=SC2046,SC2086
dc up -d --wait $(dc config --services | grep -E -- '-(regional|central|dmz)(-exporter)?$')

echo ">> cabinet leaves (+ hero exporters), batched"
up_batched "$(dc config --services | grep -E -- 'cab-.*-nats(-exporter)?$')" "leaves"

echo ">> cabinet sims, batched"
up_batched "$(dc config --services | grep -E -- '^cab-.*-sim$')" "sims"

echo ">> stream-init per DOT"
for d in gdot ncdot scdot; do
	dc run --rm "stream-init-$d"
done

echo ">> sinks (per-DOT + federation)"
dc up -d --wait gdot-sink ncdot-sink scdot-sink federation-sink

# plain `docker ps` (not `docker compose ps`): at ~100-cabinet scale compose's
# project enumeration can transiently disconnect the Docker Desktop daemon.
echo ">> demo up: $(docker ps -q --filter name=vikasa-demo | wc -l | tr -d ' ') containers running"
