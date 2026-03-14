// Package gorm provides a GORM database module for scorix applications.
// It supports ALL databases that GORM supports (SQLite, MySQL, Postgres, SQL Server, ClickHouse, etc.).
// SQLite is bundled by default. Other drivers can be registered via RegisterDriver().
//
// Enable and configure in app.yaml:
//
//	modules:
//	  gorm:
//	    enabled: true
//	    driver: sqlite               	# sqlite | mysql | postgres | sqlserver | clickhouse. Default: sqlite
//	    dsn: app.dat                 	# SQLite: filename relative to DataDir. Others: DSN string.
//	    log_level: silent            	# silent | error | warn | info. Default: silent
//	    slow_threshold_ms: 200       	# log slow queries above this threshold (ms). Default: 200
//	    prepare_stmt: true           	# cache prepared statements. Default: true
//	    skip_default_transaction: true  # skip implicit transaction on write. Default: true
//	    max_open_conns: 0            	# max open DB conns. Default: 1 (SQLite) or 10
//	    max_idle_conns: 0            	# max idle conns. Default: 1 (SQLite) or 2
//	    conn_max_lifetime_minutes: 0 	# 0 = unlimited
package gorm

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tradalab/scorix/kernel/core/module"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Config is the typed config block for the GORM module.
// All fields are optional and have sensible defaults.
type Config struct {
	// Driver is the database driver: sqlite | mysql | postgres. Default: sqlite.
	Driver string `json:"driver"`

	// DSN is the connection string (for MySQL/Postgres) or SQLite filename.
	// For SQLite, relative paths are resolved relative to ctx.DataDir.
	DSN string `json:"dsn"`

	// LogLevel controls GORM's internal logger: silent | error | warn | info
	LogLevel string `json:"log_level"`

	// SlowThresholdMs defines what counts as a "slow query" in milliseconds.
	SlowThresholdMs int `json:"slow_threshold_ms"`

	// PrepareStmt enables caching of prepared statements.
	PrepareStmt *bool `json:"prepare_stmt"`

	// SkipDefaultTransaction disables implicit transactions on write.
	// Significantly improves SQLite write throughput.
	SkipDefaultTransaction *bool `json:"skip_default_transaction"`

	// MaxOpenConns sets the max number of open DB connections.
	MaxOpenConns int `json:"max_open_conns"`

	// MaxIdleConns sets the max number of idle DB connections.
	MaxIdleConns int `json:"max_idle_conns"`

	// ConnMaxLifetimeMinutes sets the max lifetime of a connection in minutes.
	// 0 means unlimited.
	ConnMaxLifetimeMinutes int `json:"conn_max_lifetime_minutes"`
}

func (c *Config) defaults() {
	if c.Driver == "" {
		c.Driver = "sqlite"
	}
	if c.DSN == "" && c.Driver == "sqlite" {
		c.DSN = "app.dat"
	} else if c.DSN == "" {
		c.DSN = "default" // mostly just fallback
	}
	if c.LogLevel == "" {
		c.LogLevel = "silent"
	}
	if c.SlowThresholdMs == 0 {
		c.SlowThresholdMs = 200
	}
	if c.PrepareStmt == nil {
		t := true
		c.PrepareStmt = &t
	}
	if c.SkipDefaultTransaction == nil {
		t := true
		c.SkipDefaultTransaction = &t
	}
	if c.MaxOpenConns == 0 {
		if c.Driver == "sqlite" {
			c.MaxOpenConns = 1 // SQLite: single writer
		} else {
			c.MaxOpenConns = 10
		}
	}
	if c.MaxIdleConns == 0 {
		if c.Driver == "sqlite" {
			c.MaxIdleConns = 1
		} else {
			c.MaxIdleConns = 2
		}
	}
}

func (c *Config) gormLogLevel() gormlogger.LogLevel {
	switch c.LogLevel {
	case "info":
		return gormlogger.Info
	case "warn":
		return gormlogger.Warn
	case "error":
		return gormlogger.Error
	default:
		return gormlogger.Silent
	}
}

// DriverInitializer creates a gorm.Dialector from a DSN.
type DriverInitializer func(dsn string) gorm.Dialector

// ////////// Module ////////// ////////// ////////// ////////// ////////// //////////

// GormModule provides a GORM database to the application.
type GormModule struct {
	db     *gorm.DB
	mu     sync.RWMutex
	cfg    Config
	models []any          // queued for AutoMigrate in OnLoad
	seedFn func(*gorm.DB) // called in OnStart after DB is ready

	// registry of drivers: "sqlite" -> sqlite.Open
	drivers map[string]DriverInitializer

	// options hook to modify gorm.Config before open
	gormConfigHook func(*gorm.Config)
}

// New creates a new GormModule with default configuration and SQLite pre-registered.
func New() *GormModule {
	m := &GormModule{
		drivers: make(map[string]DriverInitializer),
	}
	// SQLite is registered by default because it requires special filepath handling
	// and is the primary embedded DB for Scorix.
	m.RegisterDriver("sqlite", sqlite.Open)
	return m
}

// RegisterDriver registers a new database driver initializer.
// Use this to support MySQL, Postgres, SQLServer, ClickHouse, etc. without bloating the core module.
func (m *GormModule) RegisterDriver(name string, initFn DriverInitializer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drivers[name] = initFn
}

// SetGormConfigHook allows modifying the base gorm.Config before opening the database.
func (m *GormModule) SetGormConfigHook(hook func(*gorm.Config)) {
	m.gormConfigHook = hook
}

// RegisterModel queues one or more GORM models for AutoMigrate.
// Must be called before app.Run().
func (m *GormModule) RegisterModel(models ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.models = append(m.models, models...)
}

// SetSeedFunc registers a callback invoked in OnStart after the DB is ready.
// Ideal for inserting default rows or running one-time data setup.
func (m *GormModule) SetSeedFunc(fn func(*gorm.DB)) {
	m.seedFn = fn
}

func (m *GormModule) Name() string    { return "gorm" }
func (m *GormModule) Version() string { return "1.0.0" }

// DB returns the underlying *gorm.DB. Returns nil before OnLoad completes.
func (m *GormModule) DB() *gorm.DB {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.db
}

// ////////// Lifecycle ////////// ////////// ////////// ////////// ////////// //////////

func (m *GormModule) OnLoad(ctx *module.Context) error {
	log.Printf("[gorm] loading (v%s)", m.Version())

	// Decode and apply defaults.
	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("[gorm] decode config: %w", err)
	}
	m.cfg.defaults()

	// Build GORM logger.
	glog := gormlogger.New(
		log.Default(),
		gormlogger.Config{
			SlowThreshold:             time.Duration(m.cfg.SlowThresholdMs) * time.Millisecond,
			LogLevel:                  m.cfg.gormLogLevel(),
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	m.mu.RLock()
	initFn, ok := m.drivers[m.cfg.Driver]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("[gorm] unknown driver %q. Did you call RegisterDriver?", m.cfg.Driver)
	}

	dbDesc := m.cfg.Driver
	dsn := m.cfg.DSN

	// Special handling ONLY for SQLite to resolve relative paths against DataDir
	if m.cfg.Driver == "sqlite" {
		if !filepath.IsAbs(dsn) && dsn != ":memory:" {
			dsn = filepath.Join(ctx.DataDir, dsn)
		}
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
			return fmt.Errorf("[gorm] create sqlite data dir: %w", err)
		}
		dbDesc = "sqlite at " + dsn
	}

	dialector := initFn(dsn)

	// Build base GORM config.
	gormCfg := &gorm.Config{
		PrepareStmt:            *m.cfg.PrepareStmt,
		SkipDefaultTransaction: *m.cfg.SkipDefaultTransaction,
		Logger:                 glog,
	}

	// Allow consumer to override/extend GORM config.
	if m.gormConfigHook != nil {
		m.gormConfigHook(gormCfg)
	}

	// Open database.
	db, err := gorm.Open(dialector, gormCfg)
	if err != nil {
		return fmt.Errorf("[gorm] open %s: %w", dbDesc, err)
	}

	// Configure connection pool.
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("[gorm] get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(m.cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(m.cfg.MaxIdleConns)
	if m.cfg.ConnMaxLifetimeMinutes > 0 {
		sqlDB.SetConnMaxLifetime(time.Duration(m.cfg.ConnMaxLifetimeMinutes) * time.Minute)
	}

	m.mu.Lock()
	m.db = db
	m.mu.Unlock()

	log.Printf("[gorm] connected: %s (pool max_open=%d max_idle=%d log=%s)", dbDesc, m.cfg.MaxOpenConns, m.cfg.MaxIdleConns, m.cfg.LogLevel)

	// AutoMigrate registered models.
	m.mu.RLock()
	models := m.models
	m.mu.RUnlock()
	if len(models) > 0 {
		if err := db.AutoMigrate(models...); err != nil {
			log.Println("[gorm] migrate failed: ", err)
			return fmt.Errorf("[gorm] auto-migrate: %w", err)
		}
		log.Printf("[gorm] auto-migrated %d model(s)", len(models))
	}

	// Register IPC handlers.
	module.Expose(m, "Query", ctx.IPC)
	module.Expose(m, "Exec", ctx.IPC)
	module.Expose(m, "Ping", ctx.IPC)
	module.Expose(m, "Stats", ctx.IPC)

	return nil
}

func (m *GormModule) OnStart() error {
	log.Println("[gorm] started")
	if m.seedFn != nil {
		log.Println("[gorm] running seed function")
		m.seedFn(m.db)
	}
	return nil
}

func (m *GormModule) OnStop() error {
	log.Println("[gorm] stopping")
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()

	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil
	}
	if cerr := sqlDB.Close(); cerr != nil {
		log.Printf("[gorm] close error: %v", cerr)
	}
	return nil
}

func (m *GormModule) OnUnload() error {
	log.Println("[gorm] unloaded")
	return nil
}

// ////////// Helpers ////////// ////////// ////////// ////////// ////////// //////////

func (m *GormModule) readDB() (*gorm.DB, error) {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("gorm: database not ready")
	}
	return db, nil
}

// ////////// IPC Handlers ////////// ////////// ////////// ////////// ////////// //////////

// SQLRequest represents an IPC request for executing SQL.
type SQLRequest struct {
	SQL  string `json:"sql"`
	Args []any  `json:"args,omitempty"`
}

// Query executes a raw SQL SELECT and returns rows as a slice of maps.
// JS: scorix.invoke("mod:gorm:Query", { sql: "SELECT * FROM notes WHERE id = ?", args: [1] })
func (m *GormModule) Query(ctx context.Context, req SQLRequest) ([]map[string]any, error) {
	db, err := m.readDB()
	if err != nil {
		return nil, err
	}

	rows, err := db.WithContext(ctx).Raw(req.SQL, req.Args...).Rows()
	if err != nil {
		return nil, fmt.Errorf("gorm: query: %w", err)
	}
	defer rows.Close()

	return scanRows(rows)
}

// ExecResult returned by Exec.
type ExecResult struct {
	RowsAffected int64 `json:"rows_affected"`
}

// Exec runs a write SQL statement (INSERT / UPDATE / DELETE / DDL).
// JS: scorix.invoke("mod:gorm:Exec", { sql: "DELETE FROM notes WHERE id = ?", args: [1] })
func (m *GormModule) Exec(ctx context.Context, req SQLRequest) (*ExecResult, error) {
	db, err := m.readDB()
	if err != nil {
		return nil, err
	}

	tx := db.WithContext(ctx).Exec(req.SQL, req.Args...)
	if tx.Error != nil {
		return nil, fmt.Errorf("gorm: exec: %w", tx.Error)
	}
	return &ExecResult{RowsAffected: tx.RowsAffected}, nil
}

// PingResult returned by Ping.
type PingResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// Ping checks whether the database connection is alive.
// JS: scorix.invoke("mod:gorm:Ping", null)
func (m *GormModule) Ping(ctx context.Context) (*PingResult, error) {
	db, err := m.readDB()
	if err != nil {
		return &PingResult{OK: false, Message: err.Error()}, nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return &PingResult{OK: false, Message: err.Error()}, nil
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return &PingResult{OK: false, Message: err.Error()}, nil
	}
	return &PingResult{OK: true}, nil
}

// DBStats is a subset of sql.DBStats exposed over IPC.
type DBStats struct {
	MaxOpenConnections int `json:"max_open_connections"`
	OpenConnections    int `json:"open_connections"`
	InUse              int `json:"in_use"`
	Idle               int `json:"idle"`
	WaitCount          int `json:"wait_count"`
}

// Stats returns connection pool statistics.
// JS: scorix.invoke("mod:gorm:Stats", null)
func (m *GormModule) Stats(ctx context.Context) (*DBStats, error) {
	db, err := m.readDB()
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	s := sqlDB.Stats()
	return &DBStats{
		MaxOpenConnections: s.MaxOpenConnections,
		OpenConnections:    s.OpenConnections,
		InUse:              s.InUse,
		Idle:               s.Idle,
		WaitCount:          int(s.WaitCount),
	}, nil
}

// ////////// Internal helpers ////////// ////////// ////////// ////////// ////////// //////////

// scanRows converts *sql.Rows into a slice of maps.
func scanRows(rows *sql.Rows) ([]map[string]any, error) {
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
			row[col] = vals[i]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
