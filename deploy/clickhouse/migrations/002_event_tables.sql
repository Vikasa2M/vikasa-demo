-- Envelope prefix used by every event table:
--   ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
--   ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
--   dot LowCardinality(String), district LowCardinality(String),
--   cabinet_id LowCardinality(String), device_id LowCardinality(String)
-- Engine clause used by every event table:
--   ENGINE = ReplacingMergeTree(ingested_at)
--   PARTITION BY toDate(ce_time)
--   ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
--   TTL toDateTime(ce_time) + INTERVAL 30 DAY

CREATE TABLE IF NOT EXISTS __DB__.phase_state_change (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    phase_number UInt8, to_state LowCardinality(String),
    termination_reason LowCardinality(String),
    hold_active UInt8, call_registered UInt8
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.detector_transition (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    channel UInt8, transition LowCardinality(String),
    lane LowCardinality(String), approach LowCardinality(String), phase_served UInt8
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.pedestrian_event (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    phase_number UInt8, event_kind LowCardinality(String)
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.coordination_change (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    plan_id LowCardinality(String), state LowCardinality(String)
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.preemption_event (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    preempt_number UInt8, state LowCardinality(String)
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.controller_fault_event (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    fault_id String, raised UInt8, severity LowCardinality(String)
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.operational_status (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    mode LowCardinality(String), active_plan_id LowCardinality(String)
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.reversible_lane_state (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    flow_state LowCardinality(String), open_direction LowCardinality(String),
    previous_direction LowCardinality(String), initiated_by LowCardinality(String)
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.traffic_sensor_lane_interval (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    lane_id LowCardinality(String), interval_s UInt16,
    volume UInt16, speed_kmh Float32, occupancy Float32, density Float32
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
-- lane_id appended: the sink fans one TrafficIntervalReport into N rows (one
-- per lane), all sharing the same ce_id. Without lane_id in the sort key the
-- N fanned rows are identical-key duplicates and ReplacingMergeTree collapses
-- them to one row per report on merge. A redelivered report has the same
-- ce_id AND lane_id, so idempotency on redelivery is preserved.
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id, lane_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.traffic_sensor_status (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    status LowCardinality(String)
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.queue_state (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    zone_id LowCardinality(String), queued UInt8, queue_length_m Float32
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.perception_zone_interval (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    zone_id LowCardinality(String), vehicle_class LowCardinality(String), count UInt16
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
-- zone_id, vehicle_class appended: the sink fans one ZoneIntervalReport into
-- N rows (one per zone x vehicle class), all sharing the same ce_id. Without
-- these in the sort key the N fanned rows are identical-key duplicates and
-- ReplacingMergeTree collapses them to one row per report on merge. A
-- redelivered report has the same ce_id AND zone_id/vehicle_class, so
-- idempotency on redelivery is preserved.
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id, zone_id, vehicle_class)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.perception_incident (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    incident_id String, zone_id LowCardinality(String),
    incident_type LowCardinality(String), state LowCardinality(String)  -- 'detected' | 'cleared'
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.dms_event (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    event_kind LowCardinality(String),  -- 'mode-changed' | 'fault-raised' | 'fault-cleared'
    mode LowCardinality(String), fault_id String
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS __DB__.heartbeats (
    ce_id String, ce_time DateTime64(3,'UTC'), occurred_at DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String)
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 30 DAY;

-- Skinny envelope table: one row per event of any type. Feeds the rate rollups.
CREATE TABLE IF NOT EXISTS __DB__.events_raw (
    ce_id String, ce_time DateTime64(3,'UTC'),
    ingested_at DateTime64(3,'UTC') DEFAULT now64(3),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    service LowCardinality(String), event LowCardinality(String)
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toDate(ce_time)
ORDER BY (district, cabinet_id, device_id, ce_time, ce_id)
TTL toDateTime(ce_time) + INTERVAL 7 DAY;
