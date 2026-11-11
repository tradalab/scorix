package sqlx

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	xsqlx "github.com/jmoiron/sqlx"

	"github.com/tradalab/scorix/data"
	"github.com/tradalab/scorix/logger"
)

var dialects = map[string]string{
	"sqlite3":  "sqlite3",
	"sqlite":   "sqlite3",
	"mysql":    "mysql",
	"pgx":      "postgres",
	"postgres": "postgres",
}

const keepBackups = 2

// WithMigrations runs the goose files in dir, plus the Go migrations in goose's registry, during
// OnLoad. Exclusive with WithSchema. On SQLite it fails with data.ErrNewerDatabase when the database
// is ahead of this build, and keeps a copy of the file from before each of the last 2 upgrades.
func WithMigrations(fsys fs.FS, dir string) Option {
	return func(m *Module) {
		if fsys == nil {
			return
		}
		m.initScripts = append(m.initScripts, func(ctx context.Context, db *xsqlx.DB) error {
			return runMigrations(ctx, db.DB, m.cfg.Driver, fsys, dir)
		})
	}
}

func migrations(driverName string, fsys fs.FS, dir string) (data.Migrations, error) {
	dialect, ok := dialects[driverName]
	if !ok {
		return data.Migrations{}, fmt.Errorf("[sqlx/migrate] no migration dialect for driver %q", driverName)
	}
	m := data.Migrations{FS: fsys, Dir: dir, Dialect: dialect, Registered: true}
	if dialect == "sqlite3" {
		m.RefuseNewer, m.Backups = true, keepBackups
	}
	return m, nil
}

func runMigrations(ctx context.Context, db *sql.DB, driverName string, fsys fs.FS, dir string) error {
	m, err := migrations(driverName, fsys, dir)
	if err != nil {
		return err
	}
	from, to, err := data.Migrate(ctx, db, m)
	if err != nil {
		return fmt.Errorf("[sqlx/migrate] %w", err)
	}
	if from != to {
		logger.Info(fmt.Sprintf("[sqlx/migrate] migrated %d -> %d", from, to))
	} else {
		logger.Info(fmt.Sprintf("[sqlx/migrate] up-to-date at %d", to))
	}
	return nil
}
