CREATE TABLE IF NOT EXISTS __DB__.cabinets (
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String),
    corridor LowCardinality(String),   -- '' if none, 'i85' for the ONE federation-shared cabinet per DOT
    route LowCardinality(String),      -- physical roadway: 'i85', 'i75s', '' (arterial); for corridor views
    region LowCardinality(String),     -- e.g. 'us-southeast' (subject scheme has no region token)
    vendor LowCardinality(String),
    lat Float64, lon Float64,
    updated_at DateTime64(3,'UTC') DEFAULT now64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (dot, district, cabinet_id);

CREATE TABLE IF NOT EXISTS __DB__.devices (
    dot LowCardinality(String), district LowCardinality(String),
    cabinet_id LowCardinality(String), device_id LowCardinality(String),
    device_kind LowCardinality(String),  -- asc|camera|lidar|dms|gateway
    vendor LowCardinality(String),
    updated_at DateTime64(3,'UTC') DEFAULT now64(3)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (dot, district, cabinet_id, device_id);
