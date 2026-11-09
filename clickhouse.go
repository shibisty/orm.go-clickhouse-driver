// Package clickhouse implements orm.SQLExecutor and orm.QueryExecutor for
// ClickHouse on top of database/sql and github.com/ClickHouse/clickhouse-go/v2
// (a dependency of this package, so gtr installs it).
//
// ClickHouse is an analytical database, and some things work differently:
//
//   - There is no auto-increment: the driver generates ids (orm.IDGenerator),
//     time-ordered 63-bit numbers, so Repository.Create sets ID as usual.
//   - UPDATE and DELETE are mutations: Update runs ALTER TABLE ... UPDATE and
//     Delete a lightweight DELETE; the driver sets mutations_sync=2, so both
//     wait until the change is applied. Columns of the sorting key (ORDER BY,
//     by default the primary key) cannot be updated.
//   - There are no transactions (BeginTx returns orm.ErrNotSupported), no
//     unique constraints and no foreign keys (schema errors).
//
// Tables are MergeTree by default, sorted by the primary key; see the schema
// options in schema.go.
package clickhouse

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"orm"
)

// clickhouseDriver is the orm.Driver implementation for ClickHouse.
type clickhouseDriver struct{ dsn string }

// Driver returns an orm.Driver for explicit injection:
//
//	conn, err := orm.New(ctx, clickhouse.Driver("clickhouse://default:@127.0.0.1:9000/default"))
func Driver(dsn string) orm.Driver { return clickhouseDriver{dsn: dsn} }

func (d clickhouseDriver) Open(ctx context.Context) (orm.Connection, error) { return Open(d.dsn) }

// Dialect is the ClickHouse syntax: backtick quoting, ? placeholders,
// mutations instead of UPDATE (DML) and MergeTree tables (DDL, schema.go).
type Dialect struct{}

func (Dialect) Name() string { return "clickhouse" }

func (Dialect) Quote(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}

func (Dialect) Placeholder(int) string { return "?" }

func (d Dialect) BuildSelect(q *orm.Query) (string, []any) { return orm.BuildSelectGeneric(d, q) }
func (d Dialect) BuildInsert(q *orm.Query) (string, []any) { return orm.BuildInsertGeneric(d, q) }

// BuildUpdate is ALTER TABLE t UPDATE a = ?, ... WHERE ...: ClickHouse
// updates through mutations. A mutation needs a WHERE; without conditions
// it is WHERE 1 (every row), like UPDATE without WHERE elsewhere.
func (d Dialect) BuildUpdate(q *orm.Query) (string, []any) {
	sqlText, args := orm.BuildUpdateGeneric(d, q)
	prefix := "UPDATE " + d.Quote(q.Table) + " SET "
	sqlText = "ALTER TABLE " + d.Quote(q.Table) + " UPDATE " + strings.TrimPrefix(sqlText, prefix)
	return withWhere(sqlText, q), args
}

// BuildDelete is a lightweight DELETE FROM t WHERE ... (WHERE 1 without
// conditions).
func (d Dialect) BuildDelete(q *orm.Query) (string, []any) {
	sqlText, args := orm.BuildDeleteGeneric(d, q)
	return withWhere(sqlText, q), args
}

func withWhere(sqlText string, q *orm.Query) string {
	if len(q.Wheres) == 0 {
		return sqlText + " WHERE 1"
	}
	return sqlText
}

// Conn is a ClickHouse connection pool; it implements orm.SQLExecutor,
// orm.QueryExecutor and orm.IDGenerator.
type Conn struct {
	db      *sql.DB
	dialect Dialect
	qe      orm.QueryExecutor
	ids     *idGen
}

// Open opens a connection pool from a DSN such as
// clickhouse://user:password@host:9000/database?dial_timeout=10s (the native
// protocol; http://host:8123 works too). Settings in the DSN are passed to
// the server; mutations_sync defaults to 2 so updates and deletes are
// applied before they return.
func Open(dsn string) (*Conn, error) {
	opts, err := ch.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: %w", err)
	}
	if opts.Settings == nil {
		opts.Settings = ch.Settings{}
	}
	if _, ok := opts.Settings["mutations_sync"]; !ok {
		opts.Settings["mutations_sync"] = 2
	}
	return OpenDB(ch.OpenDB(opts)), nil
}

// OpenDB wraps a *sql.DB opened with the clickhouse-go driver
// (clickhouse.OpenDB(options), sql.Open("clickhouse", dsn)).
func OpenDB(db *sql.DB) *Conn {
	c := &Conn{db: db, ids: newIDGen()}
	c.qe = orm.AsQueryExecutor(c)
	return c
}

// DB returns the underlying pool, for batch inserts and other ClickHouse
// features beyond the orm (db.Begin + Prepare("INSERT ...") is the fast
// bulk insert of clickhouse-go).
func (c *Conn) DB() *sql.DB { return c.db }

func (c *Conn) Driver() string       { return "clickhouse" }
func (c *Conn) Dialect() orm.Dialect { return c.dialect }

func (c *Conn) Ping(ctx context.Context) error { return c.db.PingContext(ctx) }
func (c *Conn) Close() error                   { return c.db.Close() }

// NewID returns a new id for Repository.Create (orm.IDGenerator).
func (c *Conn) NewID() int64 { return c.ids.next() }

func (c *Conn) ExecContext(ctx context.Context, query string, args ...any) (orm.Result, error) {
	res, err := c.db.ExecContext(ctx, query, args...)
	if err != nil {
		return orm.Result{}, err
	}
	affected, _ := res.RowsAffected() // ClickHouse does not report it for mutations
	return orm.Result{RowsAffected: affected}, nil
}

func (c *Conn) QueryContext(ctx context.Context, query string, args ...any) (orm.Rows, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (c *Conn) QueryRowContext(ctx context.Context, query string, args ...any) orm.Row {
	return c.db.QueryRowContext(ctx, query, args...)
}

// BeginTx returns orm.ErrNotSupported: ClickHouse has no transactions.
func (c *Conn) BeginTx(context.Context) (orm.Tx, error) {
	return nil, fmt.Errorf("clickhouse: transactions: %w", orm.ErrNotSupported)
}

// ---- orm.QueryExecutor ----

func (c *Conn) Select(ctx context.Context, q *orm.Query, dest any) error {
	return c.qe.Select(ctx, q, dest)
}
func (c *Conn) Insert(ctx context.Context, q *orm.Query) (orm.Result, error) {
	return c.qe.Insert(ctx, q)
}
func (c *Conn) Update(ctx context.Context, q *orm.Query) (orm.Result, error) {
	return c.qe.Update(ctx, q)
}
func (c *Conn) Delete(ctx context.Context, q *orm.Query) (orm.Result, error) {
	return c.qe.Delete(ctx, q)
}

var (
	_ orm.QueryExecutor = (*Conn)(nil)
	_ orm.SQLExecutor   = (*Conn)(nil)
	_ orm.IDGenerator   = (*Conn)(nil)
)

// idGen makes time-ordered positive int64 ids: 41 bits of milliseconds since
// 2024-01-01 (until 2093), 10 bits of a random process id, 12 bits of a
// sequence. Ids of one process never repeat; two processes collide only if
// they share the random part and the millisecond and the sequence.
type idGen struct {
	mu   sync.Mutex
	node int64
	last int64
	seq  int64
	now  func() time.Time
}

var idEpoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func newIDGen() *idGen {
	var b [2]byte
	rand.Read(b[:])
	return &idGen{node: int64(binary.BigEndian.Uint16(b[:]) & 0x3ff), now: time.Now}
}

func (g *idGen) next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	ms := g.now().Sub(idEpoch).Milliseconds()
	if ms < g.last { // the clock went back: keep counting from the last time
		ms = g.last
	}
	if ms == g.last {
		g.seq = (g.seq + 1) & 0xfff
		if g.seq == 0 { // 4096 ids in this millisecond: borrow the next one
			ms++
		}
	} else {
		g.seq = 0
	}
	g.last = ms
	return ms<<22 | g.node<<12 | g.seq
}
