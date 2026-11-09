package clickhouse

import (
	"context"
	"errors"
	"testing"
	"time"

	"orm"
	"orm/schema"
)

func TestDML(t *testing.T) {
	d := Dialect{}
	for _, c := range []struct {
		name string
		got  func() (string, []any)
		want string
		args int
	}{
		{"select", func() (string, []any) {
			return d.BuildSelect(orm.NewQuery("events").Where("kind", "=", "click").Where("id", "IN", []int64{1, 2}).OrderByDesc("id").LimitOffset(10, 20))
		}, "SELECT * FROM `events` WHERE `kind` = ? AND `id` IN (?, ?) ORDER BY `id` DESC LIMIT 10 OFFSET 20", 3},
		{"insert", func() (string, []any) {
			return d.BuildInsert(&orm.Query{Table: "events", Values: map[string]any{"id": 1, "kind": "x"}})
		}, "INSERT INTO `events` (`id`, `kind`) VALUES (?, ?)", 2},
		{"update", func() (string, []any) {
			q := &orm.Query{Table: "events", Values: map[string]any{"kind": "y", "n": 2}}
			return d.BuildUpdate(q.Where("id", "=", 1))
		}, "ALTER TABLE `events` UPDATE `kind` = ?, `n` = ? WHERE `id` = ?", 3},
		{"update all", func() (string, []any) {
			return d.BuildUpdate(&orm.Query{Table: "events", Values: map[string]any{"n": 0}})
		}, "ALTER TABLE `events` UPDATE `n` = ? WHERE 1", 1},
		{"delete", func() (string, []any) { return d.BuildDelete(orm.NewQuery("events").Where("id", "=", 1)) },
			"DELETE FROM `events` WHERE `id` = ?", 1},
		{"delete all", func() (string, []any) { return d.BuildDelete(orm.NewQuery("events")) },
			"DELETE FROM `events` WHERE 1", 0},
	} {
		sql, args := c.got()
		if sql != c.want || len(args) != c.args {
			t.Errorf("%s:\n got %s %v\nwant %s (%d args)", c.name, sql, args, c.want, c.args)
		}
	}
	if q := d.Quote("we`ird"); q != "`we``ird`" {
		t.Errorf("Quote = %s", q)
	}
}

func TestIDs(t *testing.T) {
	g := newIDGen()
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	seen := map[int64]bool{}
	prev := int64(0)
	for i := 0; i < 10000; i++ { // more than 4096 in one millisecond
		id := g.next()
		if id <= prev || seen[id] {
			t.Fatalf("id %d after %d (i=%d): not increasing", id, prev, i)
		}
		seen[id], prev = true, id
	}
	now = now.Add(-time.Hour) // the clock goes back
	if id := g.next(); id <= prev {
		t.Fatalf("after the clock went back: %d <= %d", id, prev)
	}
	if prev <= 0 || prev>>22 < now.Sub(idEpoch).Milliseconds() {
		t.Fatalf("id %d does not carry the time", prev)
	}
}

func TestNoTransactions(t *testing.T) {
	c := &Conn{}
	if _, err := c.BeginTx(context.Background()); !errors.Is(err, orm.ErrNotSupported) {
		t.Fatalf("BeginTx = %v", err)
	}
}

func TestRename(t *testing.T) {
	b := schema.New(nil, Dialect{})
	_ = b
	if s := (Dialect{}).RenameTableSQL("a", "b"); s != "RENAME TABLE `a` TO `b`" {
		t.Fatal(s)
	}
}

func TestOpenDSN(t *testing.T) {
	c, err := Open("clickhouse://default:@127.0.0.1:9/default?dial_timeout=200ms")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Driver() != "clickhouse" || c.NewID() <= 0 {
		t.Fatal("driver")
	}
	if _, err := Open("::nope"); err == nil {
		t.Fatal("bad DSN accepted")
	}
}
