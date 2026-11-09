package clickhouse

import (
	"errors"
	"fmt"
	"strings"

	"orm"
	"orm/schema"
)

// ---- schema.Dialect: DDL for ClickHouse ----
//
// Create makes a MergeTree table sorted by the primary key:
//
//	CREATE TABLE `events` (
//	  `id` Int64,
//	  `name` String,
//	  INDEX `events_name_index` (`name`) TYPE bloom_filter GRANULARITY 1
//	) ENGINE = MergeTree ORDER BY (`id`)
//
// Table options (Blueprint.Option): "engine" (default MergeTree),
// "order_by" (default the primary key, or tuple() without one),
// "partition_by", "primary_key", "ttl", "settings".
//
// Unique constraints and foreign keys do not exist in ClickHouse and are
// errors; auto-increment columns become plain integers (the driver generates
// ids). Indexes are data-skipping indexes: bloom_filter by default,
// Index.Using("minmax") picks another type.

var errNoConstraints = errors.New("ClickHouse has no unique constraints or foreign keys")

func (d Dialect) ColumnSQL(col *schema.Column) (string, error) {
	typeSQL, err := clickhouseColumnType(col)
	if err != nil {
		return "", err
	}
	if col.IsUnique {
		return "", fmt.Errorf("clickhouse: unique column %q: %w", col.Name, errNoConstraints)
	}
	if col.IsNullable {
		if strings.HasPrefix(typeSQL, "Array(") {
			return "", fmt.Errorf("clickhouse: column %q: an Array cannot be Nullable; use an empty array", col.Name)
		}
		typeSQL = "Nullable(" + typeSQL + ")"
	}
	var b strings.Builder
	b.WriteString(d.Quote(col.Name))
	b.WriteString(" ")
	b.WriteString(typeSQL)
	if col.HasDefault {
		b.WriteString(" DEFAULT ")
		b.WriteString(literal(col))
	}
	if col.CommentText != "" {
		b.WriteString(" COMMENT ")
		b.WriteString(quoteLiteral(col.CommentText))
	}
	return b.String(), nil
}

// CreateTableSQL builds the whole CREATE TABLE (schema.CreateTableDialect).
func (d Dialect) CreateTableSQL(bp *schema.Blueprint, ifNotExists bool) ([]string, error) {
	if len(bp.ForeignKeys) > 0 {
		return nil, fmt.Errorf("clickhouse: foreign key on %q: %w", bp.ForeignKeys[0].Column, errNoConstraints)
	}
	var defs, primary []string
	for _, col := range bp.Columns {
		s, err := d.ColumnSQL(col)
		if err != nil {
			return nil, fmt.Errorf("schema: column %q: %w", col.Name, err)
		}
		defs = append(defs, s)
		if col.IsPrimary {
			primary = append(primary, col.Name)
		}
	}
	for _, idx := range bp.Indexes {
		switch idx.Type {
		case schema.IdxPrimary:
			primary = append(primary, idx.Columns...)
		case schema.IdxUnique:
			return nil, fmt.Errorf("clickhouse: unique index %q: %w", idx.Name, errNoConstraints)
		case schema.IdxIndex:
			defs = append(defs, d.indexDef(idx))
		}
	}
	opt := func(k string) string { return strings.TrimSpace(bp.Options[k]) }

	engine := opt("engine")
	if engine == "" {
		engine = "MergeTree"
	}
	orderBy := opt("order_by")
	if orderBy == "" {
		orderBy = "tuple()"
		if len(primary) > 0 {
			orderBy = "(" + strings.Join(quoteAll(d, primary), ", ") + ")"
		}
	}
	var stmt strings.Builder
	stmt.WriteString("CREATE TABLE ")
	if ifNotExists {
		stmt.WriteString("IF NOT EXISTS ")
	}
	fmt.Fprintf(&stmt, "%s (\n  %s\n) ENGINE = %s", d.Quote(bp.Table), strings.Join(defs, ",\n  "), engine)
	// Engines of the MergeTree family take ORDER BY; Memory, Log and others
	// do not.
	if strings.Contains(engine, "MergeTree") {
		fmt.Fprintf(&stmt, " ORDER BY %s", orderBy)
		if p := opt("partition_by"); p != "" {
			fmt.Fprintf(&stmt, " PARTITION BY %s", p)
		}
		if p := opt("primary_key"); p != "" {
			fmt.Fprintf(&stmt, " PRIMARY KEY %s", p)
		}
		if p := opt("ttl"); p != "" {
			fmt.Fprintf(&stmt, " TTL %s", p)
		}
	}
	if p := opt("settings"); p != "" {
		fmt.Fprintf(&stmt, " SETTINGS %s", p)
	}
	return []string{stmt.String()}, nil
}

func (d Dialect) indexDef(idx *schema.Index) string {
	kind := idx.Algorithm
	if kind == "" {
		kind = "bloom_filter"
	}
	return fmt.Sprintf("INDEX %s (%s) TYPE %s GRANULARITY 1", d.Quote(idx.Name), strings.Join(quoteAll(d, idx.Columns), ", "), kind)
}

func (d Dialect) AlterColumnSQL(table string, col *schema.Column) (string, error) {
	s, err := d.ColumnSQL(col)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s", d.Quote(table), s), nil
}

func (d Dialect) TableSuffix() string { return "" } // CreateTableSQL writes ENGINE itself

// CreateIndexSQL adds a data-skipping index; it covers parts written from
// now on (ALTER TABLE ... MATERIALIZE INDEX builds it for existing data).
func (d Dialect) CreateIndexSQL(table string, idx *schema.Index) string {
	return fmt.Sprintf("ALTER TABLE %s ADD %s", d.Quote(table), d.indexDef(idx))
}

func (d Dialect) DropIndexSQL(table string, indexName string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s", d.Quote(table), d.Quote(indexName))
}

func (d Dialect) DropForeignKeySQL(string, string) (string, error) {
	return "", fmt.Errorf("clickhouse: %w", errNoConstraints)
}

// ForeignKeyClause is never used: CreateTableSQL and AddForeignKeySQL
// refuse foreign keys.
func (d Dialect) ForeignKeyClause(*schema.ForeignKey) string { return "" }

// AddUniqueSQL and AddForeignKeySQL refuse constraints in Table(...)
// (schema.AlterConstraintDialect).
func (d Dialect) AddUniqueSQL(_ string, idx *schema.Index) (string, error) {
	return "", fmt.Errorf("clickhouse: unique index %q: %w", idx.Name, errNoConstraints)
}

func (d Dialect) AddForeignKeySQL(_ string, fk *schema.ForeignKey) (string, error) {
	return "", fmt.Errorf("clickhouse: foreign key on %q: %w", fk.Column, errNoConstraints)
}

// RenameTableSQL is RENAME TABLE a TO b (schema.RenameTableDialect).
func (d Dialect) RenameTableSQL(from, to string) string {
	return fmt.Sprintf("RENAME TABLE %s TO %s", d.Quote(from), d.Quote(to))
}

// ---- type mapping ----

func clickhouseColumnType(col *schema.Column) (string, error) {
	signed := func(s, u string) string {
		if col.IsUnsigned {
			return u
		}
		return s
	}
	// Auto-increment columns (ID, Increments, ...) hold the driver's ids:
	// 63-bit numbers that scan into the int64 of orm.BaseModel.
	if col.IsAutoIncr {
		return "Int64", nil
	}
	switch col.Type {
	case schema.TypeBoolean:
		return "Bool", nil
	case schema.TypeChar, schema.TypeString, schema.TypeText, schema.TypeMediumText,
		schema.TypeLongText, schema.TypeTinyText, schema.TypeULID, schema.TypeMacAddress:
		// ClickHouse strings have no length; FixedString pads with zero
		// bytes, which a Go string would keep, so CHAR is a String too.
		return "String", nil
	case schema.TypeBinary:
		return "String", nil // bytes; scans into []byte
	case schema.TypeJSON, schema.TypeJSONB:
		return "String", nil // the JSON type is still changing between versions

	case schema.TypeTinyInteger:
		return signed("Int8", "UInt8"), nil
	case schema.TypeSmallInteger:
		return signed("Int16", "UInt16"), nil
	case schema.TypeMediumInteger, schema.TypeInteger:
		return signed("Int32", "UInt32"), nil
	case schema.TypeBigInteger:
		return signed("Int64", "UInt64"), nil
	case schema.TypeDecimal:
		return fmt.Sprintf("Decimal(%d, %d)", orDefault(col.Precision, 8), col.Scale), nil
	case schema.TypeDouble:
		return "Float64", nil
	case schema.TypeFloat:
		if col.Precision > 24 {
			return "Float64", nil
		}
		return "Float32", nil

	case schema.TypeDate:
		return "Date32", nil // 1900–2299, as other databases
	case schema.TypeDateTime, schema.TypeTimestamp:
		if col.WithTZ {
			return fmt.Sprintf("DateTime64(%d, 'UTC')", col.Precision), nil
		}
		if col.Precision > 0 {
			return fmt.Sprintf("DateTime64(%d)", col.Precision), nil
		}
		return "DateTime", nil
	case schema.TypeYear:
		return "UInt16", nil

	case schema.TypeUUID:
		return "UUID", nil
	case schema.TypeIPAddress:
		return "String", nil // IPv4 and IPv6 columns are separate types in ClickHouse

	case schema.TypeEnum:
		if len(col.Values) == 0 {
			return "", fmt.Errorf("clickhouse: enum %q has no values", col.Name)
		}
		kind := "Enum8"
		if len(col.Values) > 127 {
			kind = "Enum16"
		}
		parts := make([]string, len(col.Values))
		for i, v := range col.Values {
			parts[i] = fmt.Sprintf("%s = %d", quoteLiteral(v), i+1)
		}
		return kind + "(" + strings.Join(parts, ", ") + ")", nil
	case schema.TypeSet:
		return "Array(LowCardinality(String))", nil
	case schema.TypeVector:
		return "Array(Float32)", nil

	case schema.TypeTime:
		return "", fmt.Errorf("clickhouse: time column %q: older servers have no Time type; store seconds in an integer or a DateTime", col.Name)
	case schema.TypeGeometry, schema.TypeGeography:
		return "", fmt.Errorf("clickhouse: spatial column %q is not supported; use Point/Polygon through raw SQL", col.Name)
	default:
		return "", fmt.Errorf("clickhouse: unknown column type %q", col.Type)
	}
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func quoteAll(d Dialect, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = d.Quote(n)
	}
	return out
}

func quoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

func literal(col *schema.Column) string {
	if col.DefaultIsRaw {
		return clickhouseRaw(fmt.Sprint(col.DefaultValue))
	}
	switch v := col.DefaultValue.(type) {
	case string:
		return quoteLiteral(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case int, int64, float64:
		return fmt.Sprint(v)
	default:
		return quoteLiteral(fmt.Sprint(v))
	}
}

// clickhouseRaw translates the raw defaults migrations commonly use for
// other databases.
func clickhouseRaw(expr string) string {
	switch strings.ToUpper(strings.TrimSpace(expr)) {
	case "CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP()", "NOW()":
		return "now()"
	}
	return expr
}

// Compile-time check: Dialect implements both interfaces and the extensions.
var (
	_ schema.Dialect                = Dialect{}
	_ schema.CreateTableDialect     = Dialect{}
	_ schema.RenameTableDialect     = Dialect{}
	_ schema.AlterConstraintDialect = Dialect{}
	_ orm.Dialect                   = Dialect{}
)
