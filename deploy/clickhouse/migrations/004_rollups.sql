CREATE TABLE IF NOT EXISTS __DB__.events_1m (
    bucket DateTime('UTC'),
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), service LowCardinality(String),
    event LowCardinality(String), n UInt64
) ENGINE = SummingMergeTree(n)
PARTITION BY toDate(bucket)
ORDER BY (dot, district, cabinet_id, service, event, bucket)
TTL bucket + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS __DB__.events_1m_mv TO __DB__.events_1m AS
SELECT toStartOfMinute(ce_time) AS bucket, dot, district, cabinet_id, service, event,
       count() AS n
FROM __DB__.events_raw
GROUP BY bucket, dot, district, cabinet_id, service, event;

CREATE TABLE IF NOT EXISTS __DB__.lane_15m (
    bucket DateTime('UTC'),
    dot LowCardinality(String), cabinet_id LowCardinality(String),
    device_id LowCardinality(String), lane_id LowCardinality(String),
    volume UInt64, speed_sum Float64, samples UInt64
) ENGINE = SummingMergeTree((volume, speed_sum, samples))
PARTITION BY toDate(bucket)
ORDER BY (dot, cabinet_id, device_id, lane_id, bucket)
TTL bucket + INTERVAL 90 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS __DB__.lane_15m_mv TO __DB__.lane_15m AS
SELECT toStartOfFifteenMinutes(ce_time) AS bucket, dot, cabinet_id, device_id, lane_id,
       sum(volume) AS volume, sum(speed_kmh) AS speed_sum, count() AS samples
FROM __DB__.traffic_sensor_lane_interval
GROUP BY bucket, dot, cabinet_id, device_id, lane_id;
