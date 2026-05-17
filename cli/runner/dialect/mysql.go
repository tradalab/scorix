package dialect

import "strings"

// MySQL targets MySQL 5.7+ / MariaDB. Backticks + positional "?".
type MySQL struct{}

func (MySQL) Name() string       { return "mysql" }
func (MySQL) DriverName() string { return "mysql" }

func (MySQL) Quote(ident string) string { return "`" + ident + "`" }
func (MySQL) Placeholder(_ int) string  { return "?" }

func (MySQL) PlaceholderList(_, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func (MySQL) MapType(sqlType string, nullable bool) GoType {
	switch strings.ToUpper(strings.TrimSpace(sqlType)) {
	case "TINYINT":
		// TINYINT(1) is the BOOLEAN convention but we lack column-length info;
		// declare BOOLEAN/BOOL explicitly to get a Go bool.
		return GoType{Name: "int64"}
	case "BOOLEAN", "BOOL":
		return GoType{Name: "bool"}
	case "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT":
		return GoType{Name: "int64"}
	case "FLOAT", "DOUBLE", "DECIMAL", "NUMERIC":
		return GoType{Name: "float64"}
	case "BINARY", "VARBINARY", "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB":
		return GoType{Name: "[]byte"}
	case "DATE", "DATETIME", "TIMESTAMP", "TIME":
		if nullable {
			return GoType{Name: "sql.NullTime", NeedsSQL: true}
		}
		return GoType{Name: "time.Time", NeedsTime: true}
	case "JSON":
		return GoType{Name: "[]byte"}
	default:
		return GoType{Name: "string"}
	}
}
