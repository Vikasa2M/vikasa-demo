// Package sink converts NATS envelopes into ClickHouse rows and flushes
// them in per-table batches, only acking once every batch has landed.
package sink

import (
	"context"
	"errors"
	"fmt"

	"github.com/Vikasa2M/vikasa-demo/internal/events"
)

// Row is one insert: table + values aligned with Handler.Columns.
type Row struct {
	Table  string
	Values []any
}

// Handler describes how to turn one envelope into rows for a single
// ClickHouse table.
type Handler struct {
	Table   string
	Columns []string
	// Extract returns 1..n rows for one envelope (lane/zone reports fan out).
	Extract func(env *events.Envelope, p events.SubjectParts) ([][]any, error)
}

// Registry maps full ce-type -> Handler. Populated in handlers.go.
var Registry = map[string]Handler{}

// ErrUnknownType is returned by Rows when no handler is registered for the
// envelope's ce-type. Callers should dead-letter the envelope.
var ErrUnknownType = errors.New("sink: unknown ce-type")

// eventsRawColumns are the columns of the skinny events_raw table that every
// envelope, regardless of type, appends a row to.
var eventsRawColumns = []string{"ce_id", "ce_time", "dot", "district", "cabinet_id", "device_id", "service", "event"}

// Rows converts an envelope to rows: the type-specific handler's rows PLUS
// one events_raw row. Unknown ce-type -> ErrUnknownType (caller dead-letters).
func Rows(env *events.Envelope) ([]Row, error) {
	h, ok := Registry[env.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownType, env.Type)
	}
	// Try the 7-token internal subject form first; a federation-sink's
	// messages arrive on the DMZ-transformed 8-token share form instead
	// (vikasa.<dot>.share.<corridor>.<cabinet>.<service>.<controller>.<event>),
	// so fall back to that on failure. ce-type is carried in the header
	// (unchanged by the DMZ transform), so handler lookup above is unaffected
	// either way — only these dimension columns differ.
	parts, err := events.ParseSubject(env.Subject)
	if err != nil {
		parts, err = events.ParseShareSubject(env.Subject)
	}
	if err != nil {
		return nil, fmt.Errorf("sink: parse subject %q: %w", env.Subject, err)
	}
	typedRows, err := h.Extract(env, parts)
	if err != nil {
		return nil, fmt.Errorf("sink: extract %s: %w", env.Type, err)
	}
	rows := make([]Row, 0, len(typedRows)+1)
	for _, v := range typedRows {
		rows = append(rows, Row{Table: h.Table, Values: v})
	}
	rows = append(rows, Row{
		Table: "events_raw",
		Values: []any{
			env.ID, env.Time, parts.Dot, parts.District, parts.Cabinet, parts.Controller,
			parts.Service, parts.Event,
		},
	})
	return rows, nil
}

// Inserter abstracts clickhouse-go for tests.
type Inserter interface {
	Insert(ctx context.Context, table string, columns []string, rows [][]any) error
}

// columnsForTable finds the column list for a table, either from a
// registered handler or the events_raw special case.
func columnsForTable(table string) []string {
	if table == "events_raw" {
		return eventsRawColumns
	}
	for _, h := range Registry {
		if h.Table == table {
			return h.Columns
		}
	}
	return nil
}

// Flush groups rows per table and inserts each group. Returns an error if
// ANY group fails (caller must NOT ack).
func Flush(ctx context.Context, ins Inserter, db string, rows []Row) error {
	grouped := make(map[string][][]any)
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		if _, seen := grouped[r.Table]; !seen {
			order = append(order, r.Table)
		}
		grouped[r.Table] = append(grouped[r.Table], r.Values)
	}

	var errs []error
	for _, table := range order {
		cols := columnsForTable(table)
		if err := ins.Insert(ctx, table, cols, grouped[table]); err != nil {
			errs = append(errs, fmt.Errorf("sink: insert into %s.%s: %w", db, table, err))
		}
	}
	return errors.Join(errs...)
}
