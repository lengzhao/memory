// Package store provides database initialization and migration utilities.
package store

import (
	"fmt"
	"os"

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

// Migrate runs GORM AutoMigrate for all models
// Note: FTS5 virtual table must be created via SQL migration
func Migrate(db *gorm.DB) error {
	models := []interface{}{
		&model.MemoryItem{},
		&model.MemoryLink{},
		&model.NamespaceSummary{},
		&model.NamespacePolicy{},
		&model.MemoryEvent{},
		&model.DeletedItem{},
		// v0.3: LLM Integration
		&model.LLMConfig{},
		&model.ExtractionPrompt{},
		&model.DialogExtraction{},
		// FTSMemory is excluded - it's a virtual table
	}

	if err := db.AutoMigrate(models...); err != nil {
		return fmt.Errorf("auto migration failed: %w", err)
	}

	return nil
}

// ExecMigrationFile executes SQL statements from a migration file
func ExecMigrationFile(db *gorm.DB, filepath string) error {
	sql, err := os.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("failed to read migration file: %w", err)
	}

	// SQLite doesn't support executing multiple statements in one Exec in some drivers
	// So we need to split and execute individually
	// For simplicity, we assume the migration is valid and execute as-is
	if err := db.Exec(string(sql)).Error; err != nil {
		return fmt.Errorf("failed to execute migration: %w", err)
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
