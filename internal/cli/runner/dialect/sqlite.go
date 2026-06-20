package dialect

import "strings"

// SQLite uses backticks (matching MySQL keeps emitted code consistent) and positional "?".
type SQLite struct{}

func (SQLite) Name() string       { return "sqlite" }
func (SQLite) DriverName() string { return "sqlite3" }

func (SQLite) Quote(ident string) string { return "`" + ident + "`" }
func (SQLite) Placeholder(_ int) string  { return "?" }

func (SQLite) PlaceholderList(_, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func (SQLite) MapType(sqlType string, nullable bool) GoType {
	switch strings.ToUpper(strings.TrimSpace(sqlType)) {
	case "INTEGER", "INT", "TINYINT", "SMALLINT", "MEDIUMINT", "BIGINT":
		return GoType{Name: "int64"}
	case "REAL", "DOUBLE", "FLOAT", "DECIMAL", "NUMERIC":
		return GoType{Name: "float64"}
	case "BOOLEAN", "BOOL":
		return GoType{Name: "bool"}
	case "BLOB":
		return GoType{Name: "[]byte"}
	case "DATETIME", "TIMESTAMP", "DATE", "TIME":
		if nullable {
			return GoType{Name: "sql.NullTime", NeedsSQL: true}
		}
		return GoType{Name: "time.Time", NeedsTime: true}
	default:
		// SQLite is dynamically typed — TEXT/VARCHAR/CHAR/CLOB/unknowns → string.
		return GoType{Name: "string"}
	}
}
