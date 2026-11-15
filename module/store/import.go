package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
)

// TableDB is the part of *sql.DB, *sqlx.DB or a scorix sqlx Conn that ImportTable uses.
type TableDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

var tableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ImportTable moves a SQLite table of key, value (and deleted_at if soft deleted) into the store once: live rows
// are copied unless the store holds the key, then the table is dropped. A missing table imports 0 and a failed
// run keeps the table, so it is safe at every start. Call it after the store has loaded.
func (m *StoreModule) ImportTable(ctx context.Context, db TableDB, table string) (int, error) {
	if !tableName.MatchString(table) {
		return 0, fmt.Errorf("store: import %q: not a plain table name", table)
	}
	rows, err := db.QueryContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table)
	if err != nil {
		return 0, fmt.Errorf("store: look for table %s: %w", table, err)
	}
	var found int
	if rows.Next() {
		err = rows.Scan(&found)
	}
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, fmt.Errorf("store: look for table %s: %w", table, err)
	}
	if found == 0 {
		return 0, nil
	}

	softDelete, err := hasColumn(ctx, db, table, "deleted_at")
	if err != nil {
		return 0, err
	}
	query := `SELECT "key", "value" FROM "` + table + `"`
	if softDelete {
		query += ` WHERE deleted_at IS NULL`
	}
	rows, err = db.QueryContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("store: read %s: %w", table, err)
	}
	type row struct{ key, value string }
	var live []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.key, &r.value); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("store: read %s: %w", table, err)
		}
		live = append(live, r)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("store: read %s: %w", table, err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: read %s: %w", table, err)
	}

	moved := 0
	for _, r := range live {
		if _, ok := m.Value(r.key); ok {
			continue
		}
		if err := m.Set(ctx, map[string]any{"key": r.key, "value": r.value}); err != nil {
			return moved, fmt.Errorf("store: move %s.%s: %w", table, r.key, err)
		}
		moved++
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE "`+table+`"`); err != nil {
		return moved, fmt.Errorf("store: drop %s: %w", table, err)
	}
	return moved, nil
}

func hasColumn(ctx context.Context, db TableDB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column)
	if err != nil {
		return false, fmt.Errorf("store: read the columns of %s: %w", table, err)
	}
	defer rows.Close()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return false, fmt.Errorf("store: read the columns of %s: %w", table, err)
		}
	}
	return n > 0, rows.Err()
}
