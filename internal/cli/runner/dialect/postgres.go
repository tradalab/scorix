package dialect

import (
	"strconv"
	"strings"
)

// Postgres uses "$N" placeholders and always-quoted identifiers — Postgres folds
// unquoted idents to lower-case, so quoting preserves mixed-case schema names.
type Postgres struct{}

func (Postgres) Name() string       { return "postgres" }
func (Postgres) DriverName() string { return "pgx" }

func (Postgres) Quote(ident string) string { return `"` + ident + `"` }

func (Postgres) Placeholder(pos int) string {
	if pos < 1 {
		pos = 1
	}
	return "$" + strconv.Itoa(pos)
}

func (Postgres) PlaceholderList(start, n int) string {
	if n <= 0 {
		return ""
	}
	if start < 1 {
		start = 1
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(start + i))
	}
	return b.String()
}

func (Postgres) MapType(sqlType string, nullable bool) GoType {
	switch strings.ToUpper(strings.TrimSpace(sqlType)) {
	case "SMALLINT", "INT2", "INTEGER", "INT", "INT4", "BIGINT", "INT8",
		"SERIAL", "SERIAL4", "BIGSERIAL", "SERIAL8", "SMALLSERIAL", "SERIAL2":
		return GoType{Name: "int64"}
	case "REAL", "FLOAT4", "DOUBLE", "DOUBLE PRECISION", "FLOAT8",
		"NUMERIC", "DECIMAL":
		return GoType{Name: "float64"}
	case "BOOLEAN", "BOOL":
		return GoType{Name: "bool"}
	case "BYTEA":
		return GoType{Name: "[]byte"}
	case "DATE", "TIME", "TIMETZ", "TIMESTAMP", "TIMESTAMPTZ":
		if nullable {
			return GoType{Name: "sql.NullTime", NeedsSQL: true}
		}
		return GoType{Name: "time.Time", NeedsTime: true}
	case "JSON", "JSONB":
		return GoType{Name: "[]byte"}
	case "UUID":
		return GoType{Name: "string"}
	default:
		return GoType{Name: "string"}
	}
}
