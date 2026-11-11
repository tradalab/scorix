// Package data runs SQL for a network service through database/sql with go-zero's Session
// method set, so a model ports by changing its import. Statements and arguments go to the
// driver untouched: nothing formats them, and nothing logs them unless they are slow.
package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tradalab/scorix/observe"
)

// ErrNotFound is what QueryRowCtx returns when the query matched no row.
var ErrNotFound = errors.New("data: no rows in result set")

// Session is a connection or a transaction. dst of QueryRowCtx is a pointer to a struct
// whose db tags name every column, or to one value for a one-column result; QueryRowsCtx
// takes a pointer to a slice of either.
type Session interface {
	QueryRowCtx(ctx context.Context, dst any, query string, args ...any) error
	QueryRowsCtx(ctx context.Context, dst any, query string, args ...any) error
	ExecCtx(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Options are required except Slow: MaxOpen bounds the pool, which database/sql leaves open.
type Options struct {
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
	// Slow logs, without its arguments, every statement that takes longer; zero logs none.
	Slow time.Duration
}

type DB struct {
	db   *sql.DB
	slow time.Duration
}

// Open opens driver (registered by the caller's import) at dsn and pings it.
func Open(ctx context.Context, driver, dsn string, opt Options) (*DB, error) {
	switch {
	case opt.MaxOpen <= 0:
		return nil, errors.New("data: Options.MaxOpen is required")
	case opt.MaxIdle <= 0 || opt.MaxIdle > opt.MaxOpen:
		return nil, errors.New("data: Options.MaxIdle must be between 1 and MaxOpen")
	case opt.MaxLifetime <= 0:
		return nil, errors.New("data: Options.MaxLifetime is required")
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(opt.MaxOpen)
	db.SetMaxIdleConns(opt.MaxIdle)
	db.SetConnMaxLifetime(opt.MaxLifetime)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("data: %s: %w", driver, err)
	}
	return &DB{db: db, slow: opt.Slow}, nil
}

func (d *DB) Close() error { return d.db.Close() }

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type session struct {
	q    querier
	slow time.Duration
}

func (d *DB) session() session { return session{q: d.db, slow: d.slow} }

func (d *DB) QueryRowCtx(ctx context.Context, dst any, query string, args ...any) error {
	return d.session().QueryRowCtx(ctx, dst, query, args...)
}

func (d *DB) QueryRowsCtx(ctx context.Context, dst any, query string, args ...any) error {
	return d.session().QueryRowsCtx(ctx, dst, query, args...)
}

func (d *DB) ExecCtx(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.session().ExecCtx(ctx, query, args...)
}

// TransactCtx runs fn in one transaction: committed when fn returns nil, rolled back when
// it returns an error or panics, and the panic carried on.
func (d *DB) TransactCtx(ctx context.Context, fn func(context.Context, Session) error) (err error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
		if err != nil {
			if rerr := tx.Rollback(); rerr != nil {
				err = errors.Join(err, rerr)
			}
			return
		}
		err = tx.Commit()
	}()
	return fn(ctx, session{q: tx, slow: d.slow})
}

func (s session) timed(ctx context.Context, query string) func() {
	if s.slow <= 0 {
		return func() {}
	}
	start := time.Now()
	return func() {
		if took := time.Since(start); took > s.slow {
			observe.Log(ctx).LogAttrs(ctx, slog.LevelWarn, "slow sql", slog.String("query", query), slog.Duration("took", took))
		}
	}
}

func (s session) ExecCtx(ctx context.Context, query string, args ...any) (sql.Result, error) {
	defer s.timed(ctx, query)()
	return s.q.ExecContext(ctx, query, args...)
}

func (s session) QueryRowCtx(ctx context.Context, dst any, query string, args ...any) error {
	defer s.timed(ctx, query)()
	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanOne(rows, dst)
}

func (s session) QueryRowsCtx(ctx context.Context, dst any, query string, args ...any) error {
	defer s.timed(ctx, query)()
	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAll(rows, dst)
}
