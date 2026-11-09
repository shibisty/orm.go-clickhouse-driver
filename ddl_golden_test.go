package clickhouse_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clickhouse "orm-clickhouse"
	"orm/schema"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

// TestDDLGolden compares the DDL for every column type and common ALTERs with
// testdata/ddl.golden. After an intentional change: go test -run DDLGolden -update
func TestDDLGolden(t *testing.T) {
	b := schema.New(nil, clickhouse.Dialect{})
	var out strings.Builder

	type tc struct {
		name string
		fn   func(*schema.Blueprint)
	}
	columns := []tc{
		{"boolean", func(t *schema.Blueprint) { t.Boolean("c").Default(true) }},
		{"char", func(t *schema.Blueprint) { t.Char("c", 2) }},
		{"string", func(t *schema.Blueprint) { t.String("c", 100).Default("x") }},
		{"text", func(t *schema.Blueprint) { t.Text("c") }},
		{"mediumText", func(t *schema.Blueprint) { t.MediumText("c") }},
		{"longText", func(t *schema.Blueprint) { t.LongText("c") }},
		{"tinyText", func(t *schema.Blueprint) { t.TinyText("c") }},
		{"tinyInteger", func(t *schema.Blueprint) { t.TinyInteger("c") }},
		{"smallInteger", func(t *schema.Blueprint) { t.SmallInteger("c") }},
		{"mediumInteger", func(t *schema.Blueprint) { t.MediumInteger("c") }},
		{"integer", func(t *schema.Blueprint) { t.Integer("c").Default(0) }},
		{"bigInteger", func(t *schema.Blueprint) { t.BigInteger("c") }},
		{"unsignedInteger", func(t *schema.Blueprint) { t.UnsignedInteger("c") }},
		{"increments", func(t *schema.Blueprint) { t.Increments("c") }},
		{"id", func(t *schema.Blueprint) { t.ID() }},
		{"decimal", func(t *schema.Blueprint) { t.Decimal("c", 10, 2) }},
		{"double", func(t *schema.Blueprint) { t.Double("c") }},
		{"float", func(t *schema.Blueprint) { t.Float("c") }},
		{"date", func(t *schema.Blueprint) { t.Date("c") }},
		{"dateTime", func(t *schema.Blueprint) { t.DateTime("c", 3) }},
		{"dateTimeTz", func(t *schema.Blueprint) { t.DateTimeTz("c") }},
		{"time", func(t *schema.Blueprint) { t.Time("c") }},
		{"timestamp", func(t *schema.Blueprint) { t.Timestamp("c").DefaultRaw("CURRENT_TIMESTAMP") }},
		{"timestampTz", func(t *schema.Blueprint) { t.TimestampTz("c").Nullable() }},
		{"year", func(t *schema.Blueprint) { t.Year("c") }},
		{"binary", func(t *schema.Blueprint) { t.Binary("c") }},
		{"json", func(t *schema.Blueprint) { t.JSON("c") }},
		{"jsonb", func(t *schema.Blueprint) { t.JSONB("c") }},
		{"uuid", func(t *schema.Blueprint) { t.UUID("c") }},
		{"ulid", func(t *schema.Blueprint) { t.ULID("c") }},
		{"enum", func(t *schema.Blueprint) { t.Enum("c", []string{"a", "b"}) }},
		{"set", func(t *schema.Blueprint) { t.Set("c", []string{"a", "b"}) }},
		{"macAddress", func(t *schema.Blueprint) { t.MacAddress("c") }},
		{"ipAddress", func(t *schema.Blueprint) { t.IPAddress("c") }},
		{"geometry", func(t *schema.Blueprint) { t.Geometry("c") }},
		{"vector", func(t *schema.Blueprint) { t.Vector("c", 3) }},
		{"comment", func(t *schema.Blueprint) { t.String("c").Comment("it's") }},
		{"foreign key", func(t *schema.Blueprint) { t.ForeignID("user_id").Constrained().OnDelete("cascade") }},
		{"indexes", func(t *schema.Blueprint) { t.String("a"); t.String("b"); t.UniqueCols("a", "b"); t.IndexCols("b") }},
		{"unique column", func(t *schema.Blueprint) { t.String("c").Unique() }},
		{"skipping indexes", func(t *schema.Blueprint) {
			t.ID()
			t.String("a")
			t.DateTime("at")
			t.IndexCols("a")
			t.IndexCols("at").Using("minmax")
		}},
		{"table options", func(t *schema.Blueprint) {
			t.ID()
			t.DateTime("at").DefaultRaw("CURRENT_TIMESTAMP")
			t.Option("engine", "ReplacingMergeTree(at)")
			t.Option("order_by", "(id, at)")
			t.Option("partition_by", "toYYYYMM(at)")
			t.Option("ttl", "at + INTERVAL 30 DAY")
			t.Option("settings", "index_granularity = 4096")
		}},
		{"no primary key", func(t *schema.Blueprint) { t.String("line"); t.Option("engine", "Log") }},
		{"nullable array", func(t *schema.Blueprint) { t.Vector("c", 3).Nullable() }},
	}
	for _, c := range columns {
		stmts, err := b.SQLForCreate("t", c.fn)
		out.WriteString("-- create " + c.name + "\n")
		if err != nil {
			out.WriteString("ERROR: " + err.Error() + "\n\n")
			continue
		}
		out.WriteString(strings.Join(stmts, ";\n") + ";\n\n")
	}

	alters := []tc{
		{"add, drop, rename", func(t *schema.Blueprint) {
			t.String("nick").Nullable()
			t.DropColumn("old")
			t.RenameColumn("a", "b")
		}},
		{"change column", func(t *schema.Blueprint) { t.String("nick", 50).Change() }},
		{"indexes", func(t *schema.Blueprint) {
			t.IndexCols("nick").Using("set(100)")
			t.DropIndexCols("old")
		}},
		{"unique", func(t *schema.Blueprint) { t.UniqueCols("email") }},
		{"foreign key", func(t *schema.Blueprint) { t.ForeignID("team_id").Constrained() }},
		{"drop foreign key", func(t *schema.Blueprint) { t.DropForeignCol("company_id") }},
	}
	for _, c := range alters {
		stmts, err := b.SQLForAlter("t", c.fn)
		out.WriteString("-- alter " + c.name + "\n")
		if err != nil {
			out.WriteString("ERROR: " + err.Error() + "\n\n")
			continue
		}
		out.WriteString(strings.Join(stmts, ";\n") + ";\n\n")
	}

	golden := filepath.Join("testdata", "ddl.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if out.String() != string(want) {
		t.Errorf("DDL differs from %s; review the diff and run with -update", golden)
		t.Log(out.String())
	}
}
