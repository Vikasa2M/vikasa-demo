package sink

import (
	"context"
	"errors"
	"testing"
)

type fakeIns struct {
	calls     int
	failTable string
}

func (f *fakeIns) Insert(_ context.Context, table string, _ []string, _ [][]any) error {
	f.calls++
	if table == f.failTable {
		return errors.New("boom")
	}
	return nil
}

func TestFlushGroupsPerTable(t *testing.T) {
	ins := &fakeIns{}
	rows := []Row{{Table: "a", Values: []any{1}}, {Table: "a", Values: []any{2}}, {Table: "b", Values: []any{3}}}
	if err := Flush(context.Background(), ins, "vikasa_mardot", rows); err != nil {
		t.Fatal(err)
	}
	if ins.calls != 2 {
		t.Fatalf("want 2 grouped inserts, got %d", ins.calls)
	}
}

func TestFlushErrorPropagates(t *testing.T) {
	ins := &fakeIns{failTable: "b"}
	rows := []Row{{Table: "a", Values: []any{1}}, {Table: "b", Values: []any{2}}}
	if err := Flush(context.Background(), ins, "vikasa_mardot", rows); err == nil {
		t.Fatal("failed insert must propagate so the caller does not ack")
	}
}
