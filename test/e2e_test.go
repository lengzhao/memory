// Package test provides end-to-end tests for the memory system.
// These tests use in-memory SQLite for fast, isolated execution.
package test

import (
	"context"
	"os"
	"testing"

	"github.com/lengzhao/memory"
	"gorm.io/gorm"
)

// TestMain handles global test setup and teardown
func TestMain(m *testing.M) {
	// Run tests
	code := m.Run()
	os.Exit(code)
}

// setupTestDB creates an in-memory database for testing
func setupTestDB(t *testing.T) *TestDB {
	t.Helper()

	cfg := memory.DefaultConfig()
	cfg.Path = ":memory:"

	db, err := memory.InitDB(cfg)
	if err != nil {
		t.Fatalf("Failed to init database: %v", err)
	}

	if err := memory.Migrate(db); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	return &TestDB{DB: db}
}

// TestDB wraps a database instance with test helpers
type TestDB struct {
	DB *gorm.DB
}

// Cleanup closes the database
func (tdb *TestDB) Cleanup() {
	if tdb.DB != nil {
		memory.Close(tdb.DB)
	}
}

// context returns a background context for tests
func testContext() context.Context {
	return context.Background()
}
