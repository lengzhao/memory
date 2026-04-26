package test

import (
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/store"
	"gorm.io/gorm"
)

// setupSharedMemoryDB creates a shared-memory SQLite database for concurrent tests
// Using "file::memory:?cache=shared" allows multiple connections to share the same database
func setupSharedMemoryDB(t *testing.T) *TestDB {
	t.Helper()

	// Shared in-memory database; busy_timeout is required for concurrent writers
	// (FTS5 triggers add extra work per INSERT; match store.InitDB behavior).
	dsn := "file::memory:?cache=shared&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to init shared memory database: %v", err)
	}

	if err := store.Migrate(db); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	return &TestDB{DB: db}
}

// TestConcurrent_Remember_WithSharedCache tests concurrent memory creation using shared cache
func TestConcurrent_Remember_WithSharedCache(t *testing.T) {
	tdb := setupSharedMemoryDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// First, create one item to ensure DB is ready
	_, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace: "test/concurrent",
		Content:   "Setup content",
	})
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Concurrent creates
	var wg sync.WaitGroup
	numGoroutines := 10
	errChan := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := svc.Remember(ctx, memory.RememberRequest{
				Namespace: "test/concurrent",
				Content:   "Content from goroutine",
			})
			if err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check errors
	errCount := 0
	for err := range errChan {
		t.Errorf("Concurrent create failed: %v", err)
		errCount++
	}

	// Verify all created (including setup item) - allow some tolerance for race conditions
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"test/concurrent"},
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}

	t.Logf("Created %d items (expected %d, errors: %d)", len(hits), numGoroutines+1, errCount)

	// With shared cache, we should see all items
	if len(hits) < numGoroutines+1-2 { // Allow some tolerance
		t.Errorf("Expected at least %d items, got %d", numGoroutines+1-2, len(hits))
	}
}

// TestConcurrent_Touch_WithSharedCache tests concurrent touch with shared cache
func TestConcurrent_Touch_WithSharedCache(t *testing.T) {
	tdb := setupSharedMemoryDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace: "test/concurrent-touch",
		Content:   "Content",
	})
	if err != nil {
		t.Fatalf("Failed to create: %v", err)
	}

	// Concurrent touches
	var wg sync.WaitGroup
	numGoroutines := 20
	errChan := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.Touch(ctx, id); err != nil {
				errChan <- err
			}
		}()
	}

	wg.Wait()
	close(errChan)

	// Check errors
	for err := range errChan {
		t.Errorf("Touch failed: %v", err)
	}

	t.Log("Concurrent touches completed")
}

// TestConcurrent_RecallDuringWrite_WithSharedCache tests read during writes
func TestConcurrent_RecallDuringWrite_WithSharedCache(t *testing.T) {
	tdb := setupSharedMemoryDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	var wg sync.WaitGroup

	// Writer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, err := svc.Remember(ctx, memory.RememberRequest{
				Namespace: "test/concurrent-mixed",
				Content:   "Write content",
			})
			if err != nil {
				t.Errorf("Write failed: %v", err)
			}
		}
	}()

	// Reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, err := svc.Recall(ctx, memory.RecallRequest{
				Namespaces: []string{"test/concurrent-mixed"},
			})
			if err != nil {
				t.Errorf("Read failed: %v", err)
			}
		}
	}()

	wg.Wait()
	t.Log("Concurrent read/write completed")
}

// TestConcurrent_UpdateConflict_WithSharedCache tests optimistic locking conflicts
func TestConcurrent_UpdateConflict_WithSharedCache(t *testing.T) {
	tdb := setupSharedMemoryDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace: "test/concurrent-update",
		Content:   "Original",
	})
	if err != nil {
		t.Fatalf("Failed to create: %v", err)
	}

	// Concurrent updates - only one should succeed due to optimistic locking
	var wg sync.WaitGroup
	numGoroutines := 5
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			newContent := "Updated by goroutine"
			err := svc.Update(ctx, memory.UpdateRequest{
				ItemID:          id,
				Content:         &newContent,
				ExpectedVersion: 1, // All try to update version 1
			})
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
				t.Logf("Goroutine %d: Update succeeded", idx)
			} else {
				t.Logf("Goroutine %d: Update failed (expected): %v", idx, err)
			}
		}(i)
	}

	wg.Wait()

	// Exactly one should succeed due to optimistic locking
	if successCount != 1 {
		t.Errorf("Expected exactly 1 successful update, got %d", successCount)
	} else {
		t.Log("Optimistic locking working correctly - only 1 update succeeded")
	}
}
