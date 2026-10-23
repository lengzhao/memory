// Package store provides database initialization and migration utilities.
package store

import (
	"fmt"
	"time"

	"github.com/glebarez/sqlite" // Pure Go SQLite driver
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/lengzhao/memory/model"
)

// Config holds database configuration
type Config struct {
	Path        string
	WALEnabled  bool
	LogLevel    logger.LogLevel
	AutoMigrate bool // Automatically run Migrate on InitDB if true

	// MaxOpenConns / MaxIdleConns bound the database pool. SQLite serializes writers, so a
	// small pool plus busy_timeout is the right shape; defaults: 1 open, 1 idle.
	MaxOpenConns int
	MaxIdleConns int
	// ConnMaxLifetimeSeconds closes idle connections eventually (0 = never).
	ConnMaxLifetimeSeconds int
	// BusyTimeoutMilliseconds is how long SQLite waits for a lock before returning SQLITE_BUSY.
	// Defaults to 5000.
	BusyTimeoutMilliseconds int
}

// DefaultConfig returns default database configuration.
// AutoMigrate defaults to true for ease of use.
func DefaultConfig() Config {
	return Config{
		Path:                    "memory.db",
		WALEnabled:              true,
		LogLevel:                logger.Silent,
		AutoMigrate:             true,
		MaxOpenConns:            1,
		MaxIdleConns:            1,
		BusyTimeoutMilliseconds: 5000,
	}
}

// InitDB initializes the database connection with GORM.
// If cfg.AutoMigrate is true, it automatically runs Migrate after connection.
func InitDB(cfg Config) (*gorm.DB, error) {
	// Build DSN with pragmas
	dsn := buildDSN(cfg)

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(cfg.LogLevel),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Bound the pool: SQLite allows a single writer at a time, and an unbounded pool turns
	// concurrent writes into SQLITE_BUSY errors that callers cannot retry through.
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to access sql.DB: %w", err)
	}
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 1
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = maxOpen
	}
	sqlDB.SetMaxIdleConns(maxIdle)
	if cfg.ConnMaxLifetimeSeconds > 0 {
		sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSeconds) * time.Second)
	}

	// Enable foreign keys
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Auto migrate if requested
	if cfg.AutoMigrate {
		if err := Migrate(db); err != nil {
			return nil, fmt.Errorf("auto migration failed: %w", err)
		}
	}

	return db, nil
}

// buildDSN constructs the SQLite DSN with pragmas
func buildDSN(cfg Config) string {
	busyTimeout := cfg.BusyTimeoutMilliseconds
	if busyTimeout <= 0 {
		busyTimeout = 5000
	}
	base := fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(%s)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-20000)&_pragma=temp_store(MEMORY)&_pragma=mmap_size(268435456)&_pragma=foreign_keys(1)",
		cfg.Path,
		busyTimeout,
		map[bool]string{true: "WAL", false: "DELETE"}[cfg.WALEnabled],
	)
	return base
}

// Migrate runs GORM AutoMigrate for all models, then creates the FTS5 virtual table
// and triggers (not expressible in GORM). This is the only supported schema path.
func Migrate(db *gorm.DB) error {
	models := []interface{}{
		&model.MemoryItem{},
		&model.MemoryLink{},
		&model.NamespaceSummary{},
		&model.NamespacePolicy{},
		&model.MemoryEvent{},
		&model.DeletedItem{},
		&model.DialogExtraction{},
		&model.MemoryMerge{},
		// fts_memory: virtual table, created in installFTS5
	}

	if err := db.AutoMigrate(models...); err != nil {
		return fmt.Errorf("auto migration failed: %w", err)
	}

	// v0.3.1: dialog_hash should not be globally unique.
	// Older versions created a unique index that breaks 48h windowed idempotency.
	if err := db.Exec("DROP INDEX IF EXISTS idx_dialog_hash").Error; err != nil {
		return fmt.Errorf("drop legacy dialog hash unique index failed: %w", err)
	}

	// Extraction cache lookups are scoped by (scope_key, dialog_hash) inside a time window.
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_ext_scope_hash_created
		ON dialog_extractions (scope_key, dialog_hash, created_at)`).Error; err != nil {
		return fmt.Errorf("create extraction cache index failed: %w", err)
	}

	// Purge sweeps scan deleted_items by purge_after; the index is declared on the model
	// (idx_del_purge) and therefore managed by AutoMigrate above.

	if err := installFTS5(db); err != nil {
		return err
	}

	return nil
}

// Close closes the database connection
func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
