// Package store provides database initialization and migration utilities.
package store

import (
	"fmt"

	"github.com/glebarez/sqlite" // Pure Go SQLite driver
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/lengzhao/memory/model"
)

// Config holds database configuration
type Config struct {
	Path       string
	WALEnabled bool
	LogLevel   logger.LogLevel
}

// DefaultConfig returns default database configuration
func DefaultConfig() Config {
	return Config{
		Path:       "memory.db",
		WALEnabled: true,
		LogLevel:   logger.Silent,
	}
}

// InitDB initializes the database connection with GORM
func InitDB(cfg Config) (*gorm.DB, error) {
	// Build DSN with pragmas
	dsn := buildDSN(cfg)

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(cfg.LogLevel),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable foreign keys
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	return db, nil
}

// buildDSN constructs the SQLite DSN with pragmas
func buildDSN(cfg Config) string {
	base := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(%s)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-20000)&_pragma=temp_store(MEMORY)&_pragma=mmap_size(268435456)",
		cfg.Path,
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
		&model.LLMConfig{},
		&model.ExtractionPrompt{},
		&model.DialogExtraction{},
		&model.MemoryMerge{},
		// fts_memory: virtual table, created in installFTS5
	}

	if err := db.AutoMigrate(models...); err != nil {
		return fmt.Errorf("auto migration failed: %w", err)
	}

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
