// Package dialect provides SQL generation primitives for the model generator.
// Codegen-only — no database drivers imported here.
package dialect

import (
	"fmt"
	"strings"
)

type Dialect interface {
	Name() string
	DriverName() string
	// Quote is emitted unconditionally so reserved words (group, order, user) never collide with keywords.
	Quote(ident string) string
	// Placeholder is 1-indexed: SQLite/MySQL ignore pos ("?"); Postgres returns "$N".
	Placeholder(pos int) string
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
