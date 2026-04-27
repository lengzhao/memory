package test

import (
	"testing"

	"github.com/lengzhao/memory"
)

// TestDedupe_DedupeKey tests deduplication by dedupe_key
func TestDedupe_DedupeKey(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	dedupeKey := "unique-dedupe-key-123"

	// Create first item
	id1, err := svc.Remember(ctx, memory.RememberRequest{
		Content:   "First content",
		DedupeKey: &dedupeKey,
	})
	if err != nil {
		t.Fatalf("Failed to remember first: %v", err)
	}

	// Create second item with same dedupe_key
	id2, err := svc.Remember(ctx, memory.RememberRequest{
		Content:   "Second content (should be deduped)",
		DedupeKey: &dedupeKey,
	})
	if err != nil {
		t.Fatalf("Failed to remember second: %v", err)
	}

	// Should return same ID
	if id1 != id2 {
		t.Errorf("Expected same ID for dedupe, got %s and %s", id1, id2)
	}

	// Should only have 1 item
	hits, err := svc.Recall(ctx, memory.RecallRequest{
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("Expected 1 item after dedupe, got %d", len(hits))
	}
}

// TestDedupe_NamespaceScope tests the same dedupe string can exist in different namespaces.
func TestDedupe_NamespaceScope(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()
	dedupeKey := "shared-key-across-namespaces"

	id1, err := svc.Remember(ctx, memory.RememberRequest{
		NamespaceType: memory.NamespaceProfile,
		Content:       "A",
		DedupeKey:     &dedupeKey,
	})
	if err != nil {
		t.Fatalf("first remember: %v", err)
	}
	id2, err := svc.Remember(ctx, memory.RememberRequest{
		NamespaceType: memory.NamespaceAction,
		Content:       "B",
		DedupeKey:     &dedupeKey,
	})
	if err != nil {
		t.Fatalf("second remember: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("expected different items in different namespaces, same id %s", id1)
	}
}

// TestDedupe_EmptyDedupeKey tests empty dedupe_key behavior
// Note: Empty string is still a valid dedupe_key in current model
func TestDedupe_EmptyDedupeKey(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Test with nil dedupe_key - should allow multiple items
	id1, err := svc.Remember(ctx, memory.RememberRequest{
		Content:   "First",
		DedupeKey: nil, // nil should be ignored
	})
	if err != nil {
		t.Fatalf("Failed to remember first: %v", err)
	}

	id2, err := svc.Remember(ctx, memory.RememberRequest{
		Content:   "Second",
		DedupeKey: nil,
	})
	if err != nil {
		t.Fatalf("Failed to remember second: %v", err)
	}

	// Should be different IDs when dedupe_key is nil
	if id1 == id2 {
		t.Error("Expected different IDs when dedupe_key is nil")
	}
}
