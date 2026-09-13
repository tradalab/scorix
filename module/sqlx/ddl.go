package sqlx

import (
	"context"
	"database/sql"
	"fmt"
)

// Guarded DDL for goose migrations. SQLite has no ADD COLUMN IF NOT EXISTS, and
// one migration has to be right on two databases at once: a fresh install
// already has the column from the baseline, an older one does not.
//
// SQLite only - the other dialects need different SQL, not these.

func HasTable(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
		return false, fmt.Errorf("inspect table %s: %w", table, err)
	}
	return n > 0, nil
}

// Answers false for a table that does not exist, so a caller that cares must ask
// HasTable too.
func HasColumn(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		return false, fmt.Errorf("inspect %s.%s: %w", table, column, err)
	}
	return n > 0, nil
}

// ddl is the definition without the name, e.g. `TEXT NOT NULL DEFAULT 'manual'`.
// A missing table is skipped rather than failed: ALTER TABLE would abort the
// whole chain over a table a later migration is about to create.
func AddColumn(ctx context.Context, tx *sql.Tx, table, column, ddl string) error {
	has, err := HasColumn(ctx, tx, table, column)
	if err != nil || has {
		return err
	}
	exists, err := HasTable(ctx, tx, table)
	if err != nil || !exists {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("ALTER TABLE %q ADD COLUMN %q %s", table, column, ddl)); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

// Needs SQLite 3.35 or newer.
func DropColumn(ctx context.Context, tx *sql.Tx, table, column string) error {
	has, err := HasColumn(ctx, tx, table, column)
	if err != nil || !has {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("ALTER TABLE %q DROP COLUMN %q", table, column)); err != nil {
		return fmt.Errorf("drop %s.%s: %w", table, column, err)
	}
	return nil
}
