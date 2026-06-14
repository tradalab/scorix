// Package sqlx is the database module, runtime counterpart to `scorix generate model`.
package sqlx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // registers the default "sqlite" driver

	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/logger"
)

type Config struct {
	// Driver is the database/sql register name; "sqlite" (modernc) by default.
	// Other drivers (mysql, pgx, ...) require RegisterDriver + blank import.
	Driver string `json:"driver"`

	// DSN: SQLite uses relative filenames resolved against DataDir.
	DSN string `json:"dsn"`

	MaxOpenConns           int `json:"max_open_conns"`
	MaxIdleConns           int `json:"max_idle_conns"`
	ConnMaxLifetimeMinutes int `json:"conn_max_lifetime_minutes"`

	// SlowQueryThresholdMs logs queries exceeding this duration. 0 disables.
	SlowQueryThresholdMs int `json:"slow_query_threshold_ms"`
}

func (c *Config) defaults() {
	if c.Driver == "" {
		c.Driver = "sqlite"
	}
	if c.DSN == "" {
		if isSQLite(c.Driver) {
			c.DSN = "app.dat"
		} else {
			c.DSN = "default"
		}
	}
	if c.MaxOpenConns == 0 {
		if isSQLite(c.Driver) {
			c.MaxOpenConns = 1 // SQLite: single writer
		} else {
			c.MaxOpenConns = 10
		}
	}
	if c.MaxIdleConns == 0 {
		if isSQLite(c.Driver) {
			c.MaxIdleConns = 1
		} else {
			c.MaxIdleConns = 2
		}
	}
	if c.SlowQueryThresholdMs == 0 {
		c.SlowQueryThresholdMs = 200
	}
}

// maskDSN strips passwords for safe logging. URL form
// "scheme://user:pass@host" and MySQL "user:pass@tcp(...)" both handled;
// SQLite filenames carry no secrets and pass through unchanged.
func maskDSN(dsn string) string {
	if i := strings.Index(dsn, "://"); i >= 0 {
		rest := dsn[i+3:]
		at := strings.LastIndex(rest, "@")
		if at < 0 {
			return dsn
		}
		userinfo := rest[:at]
		colon := strings.Index(userinfo, ":")
		if colon < 0 {
			return dsn
		}
		return dsn[:i+3] + userinfo[:colon] + ":***" + rest[at:]
	}
	if at := strings.Index(dsn, "@"); at >= 0 {
		userinfo := dsn[:at]
		colon := strings.Index(userinfo, ":")
		if colon < 0 {
			return dsn
		}
		return userinfo[:colon] + ":***" + dsn[at:]
	}
	return dsn
}

func isSQLite(driver string) bool {
	return driver == "sqlite3" || driver == "sqlite"
}

type DriverInitializer func(dsn string) (*sqlx.DB, error)

type InitScript func(ctx context.Context, db *sqlx.DB) error

type Option func(*Module)

// WithSchema executes the script during OnLoad. Must be idempotent (uses
// CREATE TABLE IF NOT EXISTS etc) — re-runs on every app start. SQLite accepts
// multi-statement scripts; MySQL/Postgres callers may need to split themselves.
func WithSchema(script string) Option {
	return func(m *Module) {
		if script == "" {
			return
		}
		m.initScripts = append(m.initScripts, func(ctx context.Context, db *sqlx.DB) error {
			_, err := db.ExecContext(ctx, script)
			return err
		})
	}
}

// WithInitScript appends an arbitrary init callback when WithSchema isn't
// flexible enough (dialect-specific splitting, runtime-computed migrations).
func WithInitScript(fn InitScript) Option {
	return func(m *Module) {
		if fn == nil {
			return
		}
		m.initScripts = append(m.initScripts, fn)
	}
}

// Conn is the contract generated model code requires. Both *sqlx.DB and
// *sqlx.Tx satisfy it — Tx propagation via WithTx + From(ctx, ...) substitutes
// the Tx without changing model method signatures.
type Conn interface {
	sqlx.ExtContext
	Rebind(query string) string
}

type txCtxKey struct{}

// WithTxCtx attaches tx to ctx. Normally invoked by Module.WithTx, not directly.
func WithTxCtx(ctx context.Context, tx *sqlx.Tx) context.Context {
	return context.WithValue(ctx, txCtxKey{}, tx)
}

// From returns the Tx in ctx if any, else fallback().
func From(ctx context.Context, fallback func() Conn) Conn {
	if tx, ok := ctx.Value(txCtxKey{}).(*sqlx.Tx); ok && tx != nil {
		return tx
	}
	return fallback()
}

// WithTx runs fn in a transaction. Commit on nil return, rollback on error or
// panic. The wrapped ctx carries the Tx so generated model calls participate
// without signature changes.
func (m *Module) WithTx(ctx context.Context, fn func(context.Context) error) (err error) {
	db, dbErr := m.readDB()
	if dbErr != nil {
		return dbErr
	}
	tx, beginErr := db.BeginTxx(ctx, nil)
	if beginErr != nil {
		return fmt.Errorf("sqlx: begin tx: %w", beginErr)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(WithTxCtx(ctx, tx)); err != nil {
		return err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return fmt.Errorf("sqlx: commit tx: %w", commitErr)
	}
	return nil
}

type Module struct {
	cfg         Config
	db          *sqlx.DB
	drivers     map[string]DriverInitializer
	initScripts []InitScript
	mu          sync.RWMutex
}

// New constructs a Module with "sqlite" (modernc) pre-registered as the default
// driver. Register others via RegisterDriver + blank import.
func New(opts ...Option) *Module {
	m := &Module{
		drivers: make(map[string]DriverInitializer),
	}
	m.RegisterDriver("sqlite", defaultSqliteInit)
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func defaultSqliteInit(dsn string) (*sqlx.DB, error) {
	return sqlx.Connect("sqlite", dsn)
}

// RegisterDriver maps a driver name (must match modules.sqlx.driver in
// app.yaml) to a DSN-opener. Safe to call any time before OnLoad.
func (m *Module) RegisterDriver(name string, init DriverInitializer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drivers[name] = init
}

func (m *Module) Name() string    { return "sqlx" }
func (m *Module) Version() string { return "1.0.0" }

// DB returns nil before OnLoad completes.
func (m *Module) DB() *sqlx.DB {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.db
}

// Conn returns the DB as a Conn interface. Returns typed-nil — a bare
// *sqlx.DB(nil) cast into Conn would compare non-nil under interface rules.
func (m *Module) Conn() Conn {
	db := m.DB()
	if db == nil {
		return nil
	}
	return db
}

func (m *Module) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[sqlx] loading (v%s)", m.Version()))

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("[sqlx] decode config: %w", err)
	}
	m.cfg.defaults()

	m.mu.RLock()
	init, ok := m.drivers[m.cfg.Driver]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("[sqlx] unknown driver %q. Register it via Module.RegisterDriver before app.Run()", m.cfg.Driver)
	}

	dsn := m.cfg.DSN
	dbDesc := m.cfg.Driver

	// SQLite: relative DSN → DataDir; auto-create parent so first-run installs work.
	if isSQLite(m.cfg.Driver) {
		if !filepath.IsAbs(dsn) && dsn != ":memory:" {
			dsn = filepath.Join(ctx.DataDir, dsn)
		}
		if dsn != ":memory:" {
			if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
				return fmt.Errorf("[sqlx] create sqlite data dir: %w", err)
			}
		}
		dbDesc = "sqlite at " + dsn
	}

	db, err := init(dsn)
	if err != nil {
		return fmt.Errorf("[sqlx] open %s: %w", dbDesc, err)
	}

	db.SetMaxOpenConns(m.cfg.MaxOpenConns)
	db.SetMaxIdleConns(m.cfg.MaxIdleConns)
	if m.cfg.ConnMaxLifetimeMinutes > 0 {
		db.SetConnMaxLifetime(time.Duration(m.cfg.ConnMaxLifetimeMinutes) * time.Minute)
	}

	m.mu.Lock()
	m.db = db
	m.mu.Unlock()

	loggedDesc := dbDesc
	if !isSQLite(m.cfg.Driver) {
		loggedDesc = m.cfg.Driver + " at " + maskDSN(m.cfg.DSN)
	}
	logger.Info(fmt.Sprintf("[sqlx] connected: %s (pool max_open=%d max_idle=%d slow_query=%dms)",
		loggedDesc, m.cfg.MaxOpenConns, m.cfg.MaxIdleConns, m.cfg.SlowQueryThresholdMs))

	if len(m.initScripts) > 0 {
		loadCtx := context.Background()
		for i, fn := range m.initScripts {
			if err := fn(loadCtx, db); err != nil {
				return fmt.Errorf("[sqlx] init script %d: %w", i+1, err)
			}
		}
		logger.Info(fmt.Sprintf("[sqlx] ran %d init script(s)", len(m.initScripts)))
	}

	module.Expose(m, "Query", ctx.IPC)
	module.Expose(m, "Exec", ctx.IPC)
	module.Expose(m, "Ping", ctx.IPC)
	module.Expose(m, "Stats", ctx.IPC)

	return nil
}

func (m *Module) OnStart() error {
	logger.Info("[sqlx] started")
	return nil
}

func (m *Module) OnStop() error {
	logger.Info("[sqlx] stopping")
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return nil
	}
	if err := db.Close(); err != nil {
		logger.Error(fmt.Sprintf("[sqlx] close error: %v", err))
	}
	return nil
}

func (m *Module) OnUnload() error {
	logger.Info("[sqlx] unloaded")
	return nil
}

type SQLRequest struct {
	SQL  string `json:"sql"`
	Args []any  `json:"args,omitempty"`
}

// JS: scorix.invoke("mod:sqlx:Query", {sql, args}).
func (m *Module) Query(ctx context.Context, req SQLRequest) ([]map[string]any, error) {
	db, err := m.readDB()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	defer m.logSlow("Query", req.SQL, start)

	rows, err := db.QueryxContext(ctx, req.SQL, req.Args...)
	if err != nil {
		return nil, fmt.Errorf("sqlx: query: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

type ExecResult struct {
	RowsAffected int64 `json:"rows_affected"`
	LastInsertID int64 `json:"last_insert_id"`
}

// Exec runs INSERT/UPDATE/DELETE/DDL.
func (m *Module) Exec(ctx context.Context, req SQLRequest) (*ExecResult, error) {
	db, err := m.readDB()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	defer m.logSlow("Exec", req.SQL, start)

	res, err := db.ExecContext(ctx, req.SQL, req.Args...)
	if err != nil {
		return nil, fmt.Errorf("sqlx: exec: %w", err)
	}
	out := &ExecResult{}
	if n, e := res.RowsAffected(); e == nil {
		out.RowsAffected = n
	}
	// Postgres returns 0/error for LastInsertId — best effort, surface 0 silently.
	if id, e := res.LastInsertId(); e == nil {
		out.LastInsertID = id
	}
	return out, nil
}

type PingResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

func (m *Module) Ping(ctx context.Context) (*PingResult, error) {
	db, err := m.readDB()
	if err != nil {
		return &PingResult{OK: false, Message: err.Error()}, nil
	}
	if err := db.PingContext(ctx); err != nil {
		return &PingResult{OK: false, Message: err.Error()}, nil
	}
	return &PingResult{OK: true}, nil
}

type DBStats struct {
	MaxOpenConnections int `json:"max_open_connections"`
	OpenConnections    int `json:"open_connections"`
	InUse              int `json:"in_use"`
	Idle               int `json:"idle"`
	WaitCount          int `json:"wait_count"`
}

func (m *Module) Stats(_ context.Context) (*DBStats, error) {
	db, err := m.readDB()
	if err != nil {
		return nil, err
	}
	s := db.Stats()
	return &DBStats{
		MaxOpenConnections: s.MaxOpenConnections,
		OpenConnections:    s.OpenConnections,
		InUse:              s.InUse,
		Idle:               s.Idle,
		WaitCount:          int(s.WaitCount),
	}, nil
}

// logSlow logs queries above the threshold. SQL truncated at 512 chars; args
// never logged (may contain credentials).
func (m *Module) logSlow(op, sqlText string, start time.Time) {
	threshold := m.cfg.SlowQueryThresholdMs
	if threshold <= 0 {
		return
	}
	elapsed := time.Since(start)
	if elapsed < time.Duration(threshold)*time.Millisecond {
		return
	}
	const maxSQLLen = 512
	if len(sqlText) > maxSQLLen {
		sqlText = sqlText[:maxSQLLen] + "...(truncated)"
	}
	logger.Info(fmt.Sprintf("[sqlx] slow %s took %s: %s", op, elapsed.Round(time.Millisecond), sqlText))
}

func (m *Module) readDB() (*sqlx.DB, error) {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("sqlx: database not ready")
	}
	return db, nil
}

func scanRows(rows *sqlx.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			// SQLite returns []byte for TEXT when scanned into any — normalise
			// so the IPC payload to JS is plain string.
			if b, ok := vals[i].([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = vals[i]
			}
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
