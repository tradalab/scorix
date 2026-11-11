package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/tradalab/scorix/observe"
)

var schemaRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Migrations are goose migration files: Dir inside FS, usually an embed.FS. Dialect is
// "postgres", "sqlite3" or "mysql". Schema, Postgres only, is created when missing and holds
// the version table as <Schema>.goose_db_version, the table gokit's dbmigrate wrote.
type Migrations struct {
	FS      fs.FS
	Dir     string
	Dialect string
	Schema  string
	// Registered also runs the Go migrations added through goose.AddMigrationContext and its kin,
	// which share one registry for the whole process.
	Registered bool
	// RefuseNewer fails with ErrNewerDatabase on a version this build has no migration for. A server
	// whose old replicas keep starting through a rollout leaves it off.
	RefuseNewer bool
	// Backups, SQLite on a file only, copies the database to <file>.v<from>.<time>.bak before
	// applying anything; once an upgrade succeeds only the newest that many copies are kept.
	Backups int
}

// ErrNewerDatabase is what Migrate with RefuseNewer wraps when the database is ahead of the build.
var ErrNewerDatabase = errors.New("data: the database is newer than this build")

var dialects = map[string]goose.Dialect{
	"postgres": goose.DialectPostgres,
	"sqlite3":  goose.DialectSQLite3,
	"mysql":    goose.DialectMySQL,
}

// Migrate applies every pending migration in version order and returns the version before and
// after. On Postgres it holds an advisory lock, so replicas starting together migrate once.
func (d *DB) Migrate(ctx context.Context, m Migrations) (from, to int64, err error) {
	return Migrate(ctx, d.db, m)
}

// Migrate is (*DB).Migrate for a pool opened elsewhere.
func Migrate(ctx context.Context, db *sql.DB, m Migrations) (from, to int64, err error) {
	dialect, ok := dialects[m.Dialect]
	switch {
	case !ok:
		return 0, 0, fmt.Errorf("data: migrate: dialect %q is not postgres, sqlite3 or mysql", m.Dialect)
	case m.FS == nil:
		return 0, 0, errors.New("data: migrate: Migrations.FS is required")
	case m.Schema != "" && dialect != goose.DialectPostgres:
		return 0, 0, fmt.Errorf("data: migrate: Schema is for postgres, not %s", m.Dialect)
	case m.Schema != "" && !schemaRe.MatchString(m.Schema):
		return 0, 0, fmt.Errorf("data: migrate: schema %q is not a lower-case identifier", m.Schema)
	case m.Backups > 0 && dialect != goose.DialectSQLite3:
		return 0, 0, fmt.Errorf("data: migrate: Backups is for sqlite3, not %s", m.Dialect)
	}
	fsys := m.FS
	if m.Dir != "" && m.Dir != "." {
		if fsys, err = fs.Sub(m.FS, m.Dir); err != nil {
			return 0, 0, fmt.Errorf("data: migrate: %w", err)
		}
	}
	opts := []goose.ProviderOption{goose.WithDisableGlobalRegistry(!m.Registered), goose.WithSlog(observe.Log(ctx))}
	if m.Schema != "" {
		if err := createSchema(ctx, db, m.Schema); err != nil {
			return 0, 0, fmt.Errorf("data: migrate: schema %s: %w", m.Schema, err)
		}
		opts = append(opts, goose.WithTableName(m.Schema+".goose_db_version"))
	}
	if dialect == goose.DialectPostgres {
		locker, err := lock.NewPostgresSessionLocker()
		if err != nil {
			return 0, 0, fmt.Errorf("data: migrate: %w", err)
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}
	p, err := goose.NewProvider(dialect, db, fsys, opts...)
	if err != nil {
		return 0, 0, fmt.Errorf("data: migrate: %w", err)
	}
	if from, err = p.GetDBVersion(ctx); err != nil {
		return 0, 0, fmt.Errorf("data: migrate: %w", err)
	}
	sources := p.ListSources()
	if known := sources[len(sources)-1].Version; m.RefuseNewer && from > known {
		return from, 0, fmt.Errorf("%w: it is at version %d and this build knows up to %d", ErrNewerDatabase, from, known)
	}
	pending, err := p.HasPending(ctx)
	if err != nil {
		return from, 0, fmt.Errorf("data: migrate: %w", err)
	}
	if !pending {
		return from, from, nil
	}
	var kept, file string
	if m.Backups > 0 {
		if kept, file, err = backup(ctx, db, from); err != nil {
			return from, 0, fmt.Errorf("data: migrate: backup: %w", err)
		}
	}
	if _, err := p.Up(ctx); err != nil {
		if pe, ok := errors.AsType[*goose.PartialError](err); ok && pe.Failed != nil && pe.Failed.Source != nil {
			err = fmt.Errorf("%s: %w", pe.Failed.Source.Path, pe.Err)
		}
		if kept != "" {
			err = fmt.Errorf("%w; the database as it was before is at %s", err, kept)
		}
		return from, 0, fmt.Errorf("data: migrate: %w", err)
	}
	if to, err = p.GetDBVersion(ctx); err != nil {
		return from, 0, fmt.Errorf("data: migrate: %w", err)
	}
	if kept != "" {
		if err := prune(file, kept, m.Backups); err != nil {
			observe.Log(ctx).Warn("data: migrate: old copies were not removed", "error", err.Error())
		}
	}
	return from, to, nil
}

func backup(ctx context.Context, db *sql.DB, from int64) (path, file string, err error) {
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&file); err != nil {
		return "", "", err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name <> 'goose_db_version' AND name NOT LIKE 'sqlite\_%' ESCAPE '\'`).Scan(&tables); err != nil {
		return "", "", err
	}
	if file == "" || tables == 0 {
		return "", file, nil
	}
	path = fmt.Sprintf("%s.v%d.%s.bak", file, from, time.Now().UTC().Format(stampLayout))
	return path, file, vacuumInto(ctx, db, file, path)
}

const stampLayout = "20060102T150405.000000000"

func vacuumInto(ctx context.Context, db *sql.DB, file, path string) error {
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	f.Close()
	defer os.Remove(tmp)
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func prune(file, kept string, keep int) error {
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil {
		return err
	}
	type copyFile struct{ stamp, path string }
	var copies []copyFile
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), filepath.Base(file)+".v")
		rest, bak := strings.CutSuffix(rest, ".bak")
		version, stamp, dotted := strings.Cut(rest, ".")
		path := filepath.Join(filepath.Dir(file), e.Name())
		if _, err := strconv.ParseInt(version, 10, 64); ok && bak && dotted && err == nil && path != kept {
			copies = append(copies, copyFile{stamp, path})
		}
	}
	slices.SortFunc(copies, func(a, b copyFile) int { return strings.Compare(a.stamp, b.stamp) })
	for _, c := range copies[:max(0, len(copies)-keep+1)] {
		if err := os.Remove(c.path); err != nil {
			return err
		}
	}
	return nil
}

func createSchema(ctx context.Context, db *sql.DB, schema string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "scorix.migrate."+schema); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
		return err
	}
	return tx.Commit()
}
