package sink

import (
	"context"
	"strings"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
)

// ClickHouseInserter implements Inserter over a clickhouse-go/v2 native
// connection. Each Insert call prepares one batch for the target table,
// appends every row, and sends it. Flush calls Insert once per table and
// only reports success once every group has landed, so the caller can defer
// acking the NATS messages until the whole batch is durably written.
type ClickHouseInserter struct {
	Conn clickhouse.Conn
	DB   string
}

// asyncInsertSettings are applied to every insert on top of the big
// client-side batches consumer.go's larger BatchSize/MaxWait already
// produce. async_insert=1 lets the server coalesce this insert with any
// other concurrent small inserts (e.g. the low-volume tables, or several
// DOTs' sinks landing in vikasa_federation at once) into fewer, larger
// parts instead of one-part-per-insert, cutting merge pressure further.
// wait_for_async_insert=1 keeps Send() synchronous: it only returns once
// the server has durably queued the data and it's queryable, so the
// ack-after-flush contract in consumer.go's processBatch (ack the NATS
// messages only after Flush/Insert succeeds) still holds — a crash right
// after Send() returns can't lose rows that were never acked. The busy
// timeout is kept short so a table that rarely gets traffic (e.g.
// events_dead_letter) doesn't sit buffered for long before landing.
var asyncInsertSettings = clickhouse.Settings{
	"async_insert":                 1,
	"wait_for_async_insert":        1,
	"async_insert_busy_timeout_ms": 200,
}

// Insert satisfies the sink.Inserter interface used by Flush.
func (c *ClickHouseInserter) Insert(ctx context.Context, table string, columns []string, rows [][]any) error {
	query := "INSERT INTO " + c.DB + "." + table + " (" + strings.Join(columns, ",") + ")"
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(asyncInsertSettings))
	batch, err := c.Conn.PrepareBatch(ctx, query)
	if err != nil {
		return err
	}
	defer batch.Close()

	for _, row := range rows {
		if err := batch.Append(row...); err != nil {
			return err
		}
	}
	return batch.Send()
}
