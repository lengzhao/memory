package test

import (
	"testing"
	"time"

	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/model"
)

// TestMaintenance_PurgeDeleted tests purging soft-deleted items
func TestMaintenance_PurgeDeleted(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create and soft-delete items
	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		id, err := svc.Remember(ctx, memory.RememberRequest{
			Content:   "Content to purge",
		})
		if err != nil {
			t.Fatalf("Failed to create: %v", err)
		}
		ids[i] = id
	}

	// Soft delete all
	_, err := svc.Forget(ctx, memory.ForgetRequest{
		ItemIDs: ids,
		Mode:    "soft",
	})
	if err != nil {
		t.Fatalf("Failed to soft delete: %v", err)
	}

	// Wait a bit then purge (need to manipulate updated_at)
	time.Sleep(100 * time.Millisecond)

	// Purge items updated before now
	count, err := svc.PurgeDeleted(ctx, time.Now())
	if err != nil {
		t.Fatalf("Failed to purge: %v", err)
	}

	if count != 3 {
		t.Errorf("Expected 3 purged, got %d", count)
	}

	// Verify moved to deleted_items
	var deleted model.DeletedItem
	result := tdb.DB.WithContext(ctx).First(&deleted, "id = ?", ids[0])
	if result.Error != nil {
		t.Errorf("Expected item in deleted_items: %v", result.Error)
	}

	// Verify no longer in memory_items
	var item model.MemoryItem
	result = tdb.DB.WithContext(ctx).First(&item, "id = ?", ids[0])
	if result.Error == nil {
		t.Error("Item should not exist in memory_items after purge")
	}
}

// TestMaintenance_PurgeDeletedByTime tests purging only old deleted items
func TestMaintenance_PurgeDeletedByTime(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create and delete item
	id, _ := svc.Remember(ctx, memory.RememberRequest{
		Content:   "Old deleted content",
	})
	svc.Forget(ctx, memory.ForgetRequest{
		ItemIDs: []string{id},
		Mode:    "soft",
	})

	// Wait a moment to ensure timing difference
	time.Sleep(100 * time.Millisecond)

	// Purge items before now (should match the deleted item)
	count, err := svc.PurgeDeleted(ctx, time.Now())
	if err != nil {
		t.Fatalf("Failed to purge: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 purged (after cutoff), got %d", count)
	}

	// Verify moved to deleted_items
	var deleted model.DeletedItem
	result := tdb.DB.WithContext(ctx).First(&deleted, "id = ?", id)
	if result.Error != nil {
		t.Errorf("Expected item in deleted_items: %v", result.Error)
	}
}

// TestMaintenance_CleanupExpiredMultiple tests cleanup of multiple expired items
func TestMaintenance_CleanupExpiredMultiple(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create items with short TTL
	ttl := 1
	for i := 0; i < 5; i++ {
		_, err := svc.Remember(ctx, memory.RememberRequest{
			Content:    "Expired content",
			TTLSeconds: &ttl,
		})
		if err != nil {
			t.Fatalf("Failed to create: %v", err)
		}
	}

	// Create one non-expired item
	ttlFuture := 3600
	_, err := svc.Remember(ctx, memory.RememberRequest{
		Content:    "Non-expired content",
		TTLSeconds: &ttlFuture,
	})
	if err != nil {
		t.Fatalf("Failed to create: %v", err)
	}

	// Wait for expiry
	time.Sleep(2 * time.Second)

	// Cleanup
	count, err := svc.CleanupExpired(ctx)
	if err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
	}
	if count != 5 {
		t.Errorf("Expected 5 cleaned up, got %d", count)
	}

	// Verify statuses directly in database
	var activeCount, expiredCount int64
	tdb.DB.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("namespace = ? AND status = ?", "transient/default", model.ItemStatusActive).
		Count(&activeCount)
	tdb.DB.WithContext(ctx).Model(&model.MemoryItem{}).
		Where("namespace = ? AND status = ?", "transient/default", model.ItemStatusExpired).
		Count(&expiredCount)

	if activeCount != 1 {
		t.Errorf("Expected 1 active item, got %d", activeCount)
	}
	if expiredCount != 5 {
		t.Errorf("Expected 5 expired items, got %d", expiredCount)
	}
}

// TestMaintenance_RebuildFTS tests rebuilding FTS index
// Note: This test requires FTS5 extension to be available
func TestMaintenance_RebuildFTS(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Check if FTS5 is available
	var ftsExists int
	tdb.DB.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='fts_memory'").Scan(&ftsExists)
	if ftsExists == 0 {
		t.Skip("FTS5 not available, skipping FTS-related test")
	}

	// Create items
	for i := 0; i < 3; i++ {
		_, err := svc.Remember(ctx, memory.RememberRequest{
			Content:   "Content for FTS rebuild",
		})
		if err != nil {
			t.Fatalf("Failed to create: %v", err)
		}
	}

	// Rebuild FTS
	err := svc.RebuildFTS(ctx)
	if err != nil {
		t.Fatalf("Failed to rebuild FTS: %v", err)
	}

	// Verify items still exist
	hits, err := svc.Recall(ctx, memory.RecallRequest{
	})
	if err != nil {
		t.Fatalf("Failed to recall after rebuild: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("Expected 3 items after rebuild, got %d", len(hits))
	}
}
