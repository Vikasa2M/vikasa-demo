CREATE TABLE IF NOT EXISTS __DB__.events_dead_letter (
    received_at DateTime64(3,'UTC') DEFAULT now64(3),
    subject String, ce_id String, ce_type String, error String, payload String
) ENGINE = MergeTree
PARTITION BY toDate(received_at)
ORDER BY (received_at)
TTL toDateTime(received_at) + INTERVAL 7 DAY;
