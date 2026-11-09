//go:build integration

package clickhouse_test

import (
	"os"
	"testing"

	clickhouse "orm-clickhouse"
	"orm/drivertest"
)

// go test -tags integration ./...  with ORM_TEST_CLICKHOUSE_DSN, e.g.
// clickhouse://default:@127.0.0.1:9000/default
func TestDriver(t *testing.T) {
	dsn := os.Getenv("ORM_TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("ORM_TEST_CLICKHOUSE_DSN is not set")
	}
	conn, err := clickhouse.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	drivertest.SQLWith(t, conn, drivertest.Options{NoTransactions: true, NoUnique: true})
}
