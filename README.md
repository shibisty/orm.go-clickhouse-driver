# orm-clickhouse

ClickHouse driver for [`orm`](https://github.com/shibisty/orm.go). Go package: `clickhouse`.

Dependency: github.com/ClickHouse/clickhouse-go/v2 (wired in by the driver itself). Go 1.24+.

## Installation

```bash
gtr add github:shibisty/orm.go github:shibisty/orm.go-clickhouse-driver
```

`orm` is a peer dependency (`>=0.1.1 <0.3`): the application adds it, so all its drivers share one core.

## Usage

```go
import (
    "orm"
    clickhouse "orm-clickhouse"
    "orm/schema"
)

conn, err := clickhouse.Open("clickhouse://default:@127.0.0.1:9000/analytics")

sb, _ := schema.NewFor(conn)
err = sb.Create(ctx, "events", func(t *schema.Blueprint) {
    t.ID()
    t.String("kind")
    t.DateTime("at", 3).DefaultRaw("CURRENT_TIMESTAMP")
    t.IndexCols("kind")                         // bloom_filter skipping index
    t.Option("partition_by", "toYYYYMM(at)")
    t.Option("order_by", "(kind, at, id)")
})

events := orm.NewRepository[Event](conn)
err = events.Create(ctx, &Event{Kind: "click"}) // the driver generates ID
```

Native protocol (`clickhouse://host:9000`) and HTTP (`http://host:8123`) DSNs work;
settings in the DSN go to the server. `conn.DB()` is the `*sql.DB` for bulk inserts
(`Begin` + `Prepare("INSERT ...")` batches rows in clickhouse-go).

## How it differs from MySQL and Postgres

| | |
|---|---|
| Ids | No auto-increment: the driver generates time-ordered 63-bit ids (`orm.IDGenerator`); an id set by the application is kept. `ID()` and `Increments()` are `Int64`. |
| Update | `ALTER TABLE … UPDATE …` (a mutation). The driver sets `mutations_sync=2`, so it returns when applied. Columns of the sorting key cannot be updated. |
| Delete | Lightweight `DELETE FROM … WHERE …`. |
| Transactions | None: `BeginTx` returns `orm.ErrNotSupported`. |
| Constraints | No unique constraints or foreign keys: `Unique()`, `UniqueCols`, `ForeignID(...).Constrained()` are schema errors. |
| Tables | `ENGINE = MergeTree ORDER BY (<primary key>)` (`tuple()` without one). Options: `engine`, `order_by`, `partition_by`, `primary_key`, `ttl`, `settings`. |
| Indexes | Data-skipping indexes: `bloom_filter` by default, `IndexCols("at").Using("minmax")` for others. |
| Types | Strings have no length (`String`), `Nullable(T)` for nullable columns, `Enum8`, `Set` → `Array(LowCardinality(String))`, `Vector` → `Array(Float32)`. `Time` and spatial types are errors. |

## Tests

In a checkout, run `gtr install` once, then:

```bash
gtr run test -- -race -cover          # no database needed (DDL golden test, DML, ids)
ORM_TEST_CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9000/default" gtr run test:integration   # shared orm/drivertest suite
```

A server for the integration test: `docker run --rm -p 9000:9000 -e CLICKHOUSE_SKIP_USER_SETUP=1 clickhouse/clickhouse-server:24.8`.

After an intentional DDL change (with the `go` shim, `gtr self shims`): `go test -run DDLGolden -update .`

## License

MIT
