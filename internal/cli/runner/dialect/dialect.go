// Package dialect provides SQL generation primitives used by the model
// generator. Codegen-only — no database drivers imported here.
package dialect

import (
	"fmt"
	"strings"
)

type Dialect interface {
	// Name returns "sqlite" | "mysql" | "postgres".
	Name() string
	// DriverName returns the sql.Register name: "sqlite3" | "mysql" | "pgx".
	DriverName() string
	// Quote wraps an identifier — emitted unconditionally so reserved words
	// (group, order, user) never collide with keywords.
	Quote(ident string) string
	// Placeholder returns the marker at 1-indexed position. SQLite/MySQL
	// ignore pos and return "?"; Postgres returns "$1", "$2", etc.
	Placeholder(pos int) string
	// PlaceholderList returns n comma-separated placeholders starting at start.
	PlaceholderList(start, n int) string
	MapType(sqlType string, nullable bool) GoType
}

type GoType struct {
	Name      string
	NeedsTime bool // requires "time" import
	NeedsSQL  bool // requires "database/sql" import (sql.NullXxx)
}

// New accepts common aliases ("pg" → "postgres", "sqlite3" → "sqlite").
func New(name string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "sqlite", "sqlite3":
		return SQLite{}, nil
	case "mysql", "mariadb":
		return MySQL{}, nil
	case "postgres", "postgresql", "pg":
		return Postgres{}, nil
	default:
		return nil, fmt.Errorf("dialect: unknown name %q (want sqlite|mysql|postgres)", name)
	}
}

func MustNew(name string) Dialect {
	d, err := New(name)
	if err != nil {
		panic(err)
	}
	return d
}
