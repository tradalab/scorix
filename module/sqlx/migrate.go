package sqlx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	xsqlx "github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"

	"github.com/tradalab/scorix/logger"
)

// gooseDialect maps our driver names to goose's dialect tokens. Goose has its
// own taxonomy — "sqlite3"/"sqlite" both go to "sqlite3"; "pgx" → "postgres".
var gooseDialect = map[string]string{
	"sqlite3":  "sqlite3",
	"sqlite":   "sqlite3",
	"mysql":    "mysql",
	"pgx":      "postgres",
	"postgres": "postgres",
}

// WithMigrations runs `goose up` against the given migration directory during
// OnLoad. Idempotent — already-applied versions skipped. Mutually exclusive
// with WithSchema: mixing makes goose's version table fight schema.sql's
// CREATE TABLE IF NOT EXISTS statements.
func WithMigrations(fsys fs.FS, dir string) Option {
	return func(m *Module) {
		if fsys == nil {
			return
		}
		m.initScripts = append(m.initScripts, func(ctx context.Context, db *xsqlx.DB) error {
			return runGooseUp(ctx, db.DB, m.cfg.Driver, fsys, dir)
		})
	}
}

// runGooseUp is split out so tests can exercise the wiring without OnLoad.
func runGooseUp(ctx context.Context, db *sql.DB, driverName string, fsys fs.FS, dir string) error {
	dialect, ok := gooseDialect[driverName]
	if !ok {
		return fmt.Errorf("[sqlx/migrate] no goose dialect mapping for driver %q", driverName)
	}
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("[sqlx/migrate] set dialect: %w", err)
	}
	goose.SetBaseFS(fsys)
	defer goose.SetBaseFS(nil)

	goose.SetLogger(goose.NopLogger())

	current, err := goose.GetDBVersionContext(ctx, db)
	if err != nil && !errors.Is(err, goose.ErrNoNextVersion) {
		// First run — goose creates the version table on Up.
		current = 0
	}
	logger.Info(fmt.Sprintf("[sqlx/migrate] current version=%d dir=%s", current, dir))

	if err := goose.UpContext(ctx, db, dir); err != nil {
		return fmt.Errorf("[sqlx/migrate] up: %w", err)
	}

	after, _ := goose.GetDBVersionContext(ctx, db)
	if after != current {
		logger.Info(fmt.Sprintf("[sqlx/migrate] migrated %d → %d", current, after))
	} else {
		logger.Info("[sqlx/migrate] up-to-date")
	}
	return nil
}
