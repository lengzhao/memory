// Package store provides test utilities.
package store

import (
	"os"
	"testing"

	"gorm.io/gorm"
)

// SetupTestDB creates a test database (in-memory or temp file).
func SetupTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	// Use temp file for better compatibility
	tmpFile := t.TempDir() + "/test.db"

	cfg := Config{
		Path:       tmpFile,
		WALEnabled: false, // Disable WAL for tests
		LogLevel:   1,     // Silent
	}

	db, err := InitDB(cfg)
	if err != nil {
		t.Fatalf("Failed to init test DB: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	t.Cleanup(func() {
		Close(db)
		os.Remove(tmpFile)
	})

	return db
}
