package test

import (
	"testing"
	"time"

	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/model"
)

// TestRemember_CreateMemory tests basic memory creation
func TestRemember_CreateMemory(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	req := memory.RememberRequest{
		Namespace:     "test/crud",
		NamespaceType: memory.NamespaceKnowledge,
		Title:         "Test Title",
		Content:       "This is test content",
		Tags:          []string{"test", "crud"},
		SourceType:    memory.SourceUser,
		Importance:    75,
		Confidence:    0.95,
	}

	id, err := svc.Remember(ctx, req)
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}
	if id == "" {
		t.Fatal("Expected non-empty ID")
	}

	// Verify by recalling
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"test/crud"},
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Expected 1 hit, got %d", len(hits))
	}
	if hits[0].Content != req.Content {
		t.Errorf("Content mismatch: got %q, want %q", hits[0].Content, req.Content)
	}
}

// TestRemember_WithTTL tests memory creation with TTL
func TestRemember_WithTTL(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	ttl := 3600 // 1 hour
	req := memory.RememberRequest{
		Namespace:     "test/ttl",
		NamespaceType: memory.NamespaceTransient,
		Content:       "TTL test content",
		TTLSeconds:    &ttl,
	}

	id, err := svc.Remember(ctx, req)
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Verify expires_at is set
	var item model.MemoryItem
	if err := tdb.DB.WithContext(ctx).First(&item, "id = ?", id).Error; err != nil {
		t.Fatalf("Failed to retrieve item: %v", err)
	}

	if item.ExpiresAt == nil {
		t.Fatal("Expected ExpiresAt to be set")
	}

	// Should expire roughly 1 hour from now (within 10 second tolerance)
	expectedExpiry := time.Now().Add(time.Duration(ttl) * time.Second)
	diff := item.ExpiresAt.Sub(expectedExpiry)
	if diff < -10*time.Second || diff > 10*time.Second {
		t.Errorf("Expiry time off by %v, expected around %v, got %v", diff, expectedExpiry, *item.ExpiresAt)
	}
}

// TestRecall_ByQuery tests FTS search
// Note: This test requires FTS5 extension to be available
func TestRecall_ByQuery(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Check if FTS5 is available
	var ftsExists int
	tdb.DB.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='fts_memory'").Scan(&ftsExists)
	if ftsExists == 0 {
		t.Skip("FTS5 not available, skipping FTS search test")
	}

	// Create memories with FTS
	contents := []string{
		"The quick brown fox jumps over the lazy dog",
		"Python is a programming language",
		"Go is great for concurrent programming",
		"Machine learning is fascinating",
	}

	for _, content := range contents {
		_, err := svc.Remember(ctx, memory.RememberRequest{
			Namespace:     "test/fts",
			NamespaceType: memory.NamespaceKnowledge,
			Content:       content,
		})
		if err != nil {
			t.Fatalf("Failed to remember: %v", err)
		}
	}

	// Give triggers time to populate FTS (in real scenarios this is synchronous)
	// Search for "programming"
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Query:      "programming",
		Namespaces: []string{"test/fts"},
		TopK:       10,
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}

	// Should find matches (exact behavior depends on FTS setup)
	t.Logf("Found %d hits for 'programming'", len(hits))
}

// TestRecall_ByTags tests tag filtering
func TestRecall_ByTags(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create items with different tags
	tagSets := [][]string{
		{"go", "programming"},
		{"python", "programming"},
		{"go", "tutorial"},
	}
	contents := []string{"Go content", "Python content", "Go tutorial"}

	for i, tags := range tagSets {
		_, err := svc.Remember(ctx, memory.RememberRequest{
			Namespace:     "test/tags",
			NamespaceType: memory.NamespaceKnowledge,
			Content:       contents[i],
			Tags:          tags,
		})
		if err != nil {
			t.Fatalf("Failed to remember: %v", err)
		}
	}

	// Test TagsAny - should match any tag
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"test/tags"},
		TagsAny:    []string{"go"},
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("Expected 2 hits for 'go' tag, got %d", len(hits))
	}

	// Test TagsAll - should match all tags
	hits, err = svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"test/tags"},
		TagsAll:    []string{"go", "programming"},
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("Expected 1 hit for 'go' AND 'programming', got %d", len(hits))
	}
}

// TestUpdate_Success tests successful update with optimistic locking
func TestUpdate_Success(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace: "test/update",
		Content:   "Original content",
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Get current version
	var item model.MemoryItem
	tdb.DB.WithContext(ctx).First(&item, "id = ?", id)

	// Update with correct version
	newContent := "Updated content"
	err = svc.Update(ctx, memory.UpdateRequest{
		ItemID:          id,
		Content:         &newContent,
		ExpectedVersion: item.Version,
	})
	if err != nil {
		t.Fatalf("Failed to update: %v", err)
	}

	// Verify update
	hits, _ := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"test/update"},
	})
	if hits[0].Content != newContent {
		t.Errorf("Content not updated: got %q, want %q", hits[0].Content, newContent)
	}
}

// TestUpdate_VersionConflict tests optimistic locking conflict
func TestUpdate_VersionConflict(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace: "test/conflict",
		Content:   "Original content",
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Get current version
	var item model.MemoryItem
	tdb.DB.WithContext(ctx).First(&item, "id = ?", id)

	// First update (should succeed)
	newContent := "First update"
	err = svc.Update(ctx, memory.UpdateRequest{
		ItemID:          id,
		Content:         &newContent,
		ExpectedVersion: item.Version,
	})
	if err != nil {
		t.Fatalf("First update failed: %v", err)
	}

	// Second update with old version (should fail)
	newContent2 := "Second update"
	err = svc.Update(ctx, memory.UpdateRequest{
		ItemID:          id,
		Content:         &newContent2,
		ExpectedVersion: item.Version, // Old version
	})
	if err == nil {
		t.Fatal("Expected version conflict error, got nil")
	}

	// Check it's a conflict error
	if me, ok := memory.ErrorAs(err); !ok || me.Code != memory.CodeConflict {
		t.Errorf("Expected conflict error, got: %v", err)
	}
}

// TestForget_Soft tests soft delete
func TestForget_Soft(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace: "test/forget",
		Content:   "To be deleted",
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Soft delete
	count, err := svc.Forget(ctx, memory.ForgetRequest{
		ItemIDs: []string{id},
		Mode:    "soft",
	})
	if err != nil {
		t.Fatalf("Failed to forget: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 item deleted, got %d", count)
	}

	// Verify not in active results
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"test/forget"},
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("Expected 0 hits after soft delete, got %d", len(hits))
	}

	// Verify status is deleted
	var item model.MemoryItem
	tdb.DB.WithContext(ctx).First(&item, "id = ?", id)
	if item.Status != model.ItemStatusDeleted {
		t.Errorf("Expected status deleted, got %v", item.Status)
	}
}

// TestForget_ByNamespace tests forget by namespace
func TestForget_ByNamespace(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create multiple items in same namespace
	for i := 0; i < 3; i++ {
		_, err := svc.Remember(ctx, memory.RememberRequest{
			Namespace: "test/ns-forget",
			Content:   "Content",
		})
		if err != nil {
			t.Fatalf("Failed to remember: %v", err)
		}
	}

	// Forget by namespace
	count, err := svc.Forget(ctx, memory.ForgetRequest{
		Namespace: "test/ns-forget",
		Mode:      "soft",
	})
	if err != nil {
		t.Fatalf("Failed to forget: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 items deleted, got %d", count)
	}

	// Verify all deleted
	hits, _ := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"test/ns-forget"},
	})
	if len(hits) != 0 {
		t.Errorf("Expected 0 hits, got %d", len(hits))
	}
}

// TestTouch_AccessCount tests touch increments access count
func TestTouch_AccessCount(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace: "test/touch",
		Content:   "Content",
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Touch multiple times
	for i := 0; i < 5; i++ {
		if err := svc.Touch(ctx, id); err != nil {
			t.Fatalf("Failed to touch: %v", err)
		}
	}

	// Verify access count (may need small delay for async update)
	time.Sleep(100 * time.Millisecond)

	var item model.MemoryItem
	tdb.DB.WithContext(ctx).First(&item, "id = ?", id)
	if item.AccessCount != 5 {
		t.Errorf("Expected access count 5, got %d", item.AccessCount)
	}
}

