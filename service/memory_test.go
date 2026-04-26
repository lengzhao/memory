// Package service provides tests for MemoryService.
package service

import (
	"context"
	"testing"
	"time"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/store"
)

func TestMemoryService_Remember(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	ctx := context.Background()

	t.Run("basic remember", func(t *testing.T) {
		req := RememberRequest{
			Namespace:     "test/basic",
			NamespaceType: model.NamespaceTypeTransient,
			Title:         "Test Memory",
			Content:       "This is a test memory content.",
			SourceType:    model.SourceTypeUser,
			Importance:    50,
			Confidence:    0.9,
		}

		id, err := svc.Remember(ctx, req)
		if err != nil {
			t.Fatalf("Remember failed: %v", err)
		}
		if id == "" {
			t.Fatal("Expected non-empty ID")
		}
		if len(id) != 26 { // ULID length
			t.Fatalf("Expected ULID length 26, got %d", len(id))
		}
	})

	t.Run("dedupe key prevents duplicate", func(t *testing.T) {
		key := "unique-key-123"
		req := RememberRequest{
			Namespace:     "test/dedupe",
			NamespaceType: model.NamespaceTypeTransient,
			Content:       "Original content",
			DedupeKey:     &key,
		}

		id1, err := svc.Remember(ctx, req)
		if err != nil {
			t.Fatalf("First remember failed: %v", err)
		}

		// Same dedupe key should return same ID
		req.Content = "Different content"
		id2, err := svc.Remember(ctx, req)
		if err != nil {
			t.Fatalf("Second remember failed: %v", err)
		}

		if id1 != id2 {
			t.Fatalf("Expected same ID for dedupe, got %s and %s", id1, id2)
		}
	})

	t.Run("with TTL", func(t *testing.T) {
		ttl := 3600 // 1 hour
		req := RememberRequest{
			Namespace:     "test/ttl",
			NamespaceType: model.NamespaceTypeTransient,
			Content:       "TTL test",
			TTLSeconds:    &ttl,
		}

		id, err := svc.Remember(ctx, req)
		if err != nil {
			t.Fatalf("Remember with TTL failed: %v", err)
		}

		// Verify expires_at is set
		var item model.MemoryItem
		if err := db.First(&item, "id = ?", id).Error; err != nil {
			t.Fatalf("Failed to query item: %v", err)
		}
		if item.ExpiresAt == nil {
			t.Fatal("Expected expires_at to be set")
		}
	})
}

func TestMemoryService_Recall(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	ctx := context.Background()

	// Seed data
	for i := 0; i < 5; i++ {
		_, _ = svc.Remember(ctx, RememberRequest{
			Namespace:     "test/recall",
			NamespaceType: model.NamespaceTypeKnowledge,
			Title:         "Memory " + string(rune('A'+i)),
			Content:       "Content about Go programming language.",
			Tags:          []string{"go", "programming"},
			Importance:    50 + i*10,
			Confidence:    0.9,
		})
	}

	t.Run("basic recall", func(t *testing.T) {
		req := RecallRequest{
			TopK: 10,
		}
		hits, err := svc.Recall(ctx, req)
		if err != nil {
			t.Fatalf("Recall failed: %v", err)
		}
		if len(hits) != 5 {
			t.Fatalf("Expected 5 hits, got %d", len(hits))
		}
	})

	t.Run("namespace filter", func(t *testing.T) {
		req := RecallRequest{
			Namespaces: []string{"test/recall"},
			TopK:       10,
		}
		hits, err := svc.Recall(ctx, req)
		if err != nil {
			t.Fatalf("Recall failed: %v", err)
		}
		if len(hits) != 5 {
			t.Fatalf("Expected 5 hits, got %d", len(hits))
		}
	})

	t.Run("tag filter", func(t *testing.T) {
		req := RecallRequest{
			Namespaces: []string{"test/recall"},
			TagsAny:    []string{"go"},
			TopK:       10,
		}
		hits, err := svc.Recall(ctx, req)
		if err != nil {
			t.Fatalf("Recall failed: %v", err)
		}
		if len(hits) != 5 {
			t.Fatalf("Expected 5 hits with tag 'go', got %d", len(hits))
		}
	})
}

func TestMemoryService_List(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	ctx := context.Background()

	create := func(title string) {
		_, err := svc.Remember(ctx, RememberRequest{
			Namespace:     "test/list",
			NamespaceType: model.NamespaceTypeKnowledge,
			Title:         title,
			Content:       "List ordering test",
			SourceType:    model.SourceTypeUser,
			Importance:    50,
			Confidence:    0.9,
		})
		if err != nil {
			t.Fatalf("Remember failed: %v", err)
		}
	}

	create("first")
	time.Sleep(5 * time.Millisecond)
	create("second")
	time.Sleep(5 * time.Millisecond)
	create("third")

	t.Run("default desc returns newest first", func(t *testing.T) {
		items, err := svc.List(ctx, ListRequest{
			Namespaces: []string{"test/list"},
			TopK:       2,
		})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("Expected 2 items, got %d", len(items))
		}
		if items[0].Title != "third" || items[1].Title != "second" {
			t.Fatalf("Unexpected order: got [%s, %s]", items[0].Title, items[1].Title)
		}
	})

	t.Run("asc returns oldest first", func(t *testing.T) {
		items, err := svc.List(ctx, ListRequest{
			Namespaces: []string{"test/list"},
			TopK:       2,
			Order:      "asc",
		})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("Expected 2 items, got %d", len(items))
		}
		if items[0].Title != "first" || items[1].Title != "second" {
			t.Fatalf("Unexpected order: got [%s, %s]", items[0].Title, items[1].Title)
		}
	})
}

func TestMemoryService_Update(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	ctx := context.Background()

	// Create item
	id, _ := svc.Remember(ctx, RememberRequest{
		Namespace:     "test/update",
		NamespaceType: model.NamespaceTypeTransient,
		Title:         "Original",
		Content:       "Original content",
	})

	// Get current version
	var item model.MemoryItem
	db.First(&item, "id = ?", id)

	t.Run("successful update", func(t *testing.T) {
		newTitle := "Updated"
		req := UpdateRequest{
			ItemID:          id,
			Title:           &newTitle,
			ExpectedVersion: item.Version,
		}

		err := svc.Update(ctx, req)
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		// Verify update
		var updated model.MemoryItem
		db.First(&updated, "id = ?", id)
		if updated.Title != "Updated" {
			t.Fatalf("Expected title 'Updated', got '%s'", updated.Title)
		}
		if updated.Version != item.Version+1 {
			t.Fatalf("Expected version %d, got %d", item.Version+1, updated.Version)
		}
	})

	t.Run("version conflict", func(t *testing.T) {
		newTitle := "Should Fail"
		req := UpdateRequest{
			ItemID:          id,
			Title:           &newTitle,
			ExpectedVersion: 1, // Old version
		}

		err := svc.Update(ctx, req)
		if err == nil {
			t.Fatal("Expected version conflict error")
		}
	})
}

func TestMemoryService_Forget(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	ctx := context.Background()

	// Create items
	var ids []string
	for i := 0; i < 3; i++ {
		id, _ := svc.Remember(ctx, RememberRequest{
			Namespace:     "test/forget",
			NamespaceType: model.NamespaceTypeTransient,
			Content:       "To be deleted",
		})
		ids = append(ids, id)
	}

	t.Run("soft delete", func(t *testing.T) {
		req := ForgetRequest{
			ItemIDs: ids[:1],
			Mode:    "soft",
		}
		count, err := svc.Forget(ctx, req)
		if err != nil {
			t.Fatalf("Forget failed: %v", err)
		}
		if count != 1 {
			t.Fatalf("Expected 1 deleted, got %d", count)
		}

		// Verify status
		var item model.MemoryItem
		db.First(&item, "id = ?", ids[0])
		if item.Status != model.ItemStatusDeleted {
			t.Fatalf("Expected status deleted, got %s", item.Status)
		}
	})

	t.Run("expire", func(t *testing.T) {
		req := ForgetRequest{
			ItemIDs: ids[1:2],
			Mode:    "expire",
		}
		count, err := svc.Forget(ctx, req)
		if err != nil {
			t.Fatalf("Expire failed: %v", err)
		}
		if count != 1 {
			t.Fatalf("Expected 1 expired, got %d", count)
		}

		var item model.MemoryItem
		db.First(&item, "id = ?", ids[1])
		if item.Status != model.ItemStatusExpired {
			t.Fatalf("Expected status expired, got %s", item.Status)
		}
	})
}

func TestMemoryService_Touch(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	ctx := context.Background()

	id, _ := svc.Remember(ctx, RememberRequest{
		Namespace:     "test/touch",
		NamespaceType: model.NamespaceTypeTransient,
		Content:       "Test touch",
	})

	t.Run("touch updates access", func(t *testing.T) {
		err := svc.Touch(ctx, id)
		if err != nil {
			t.Fatalf("Touch failed: %v", err)
		}

		var item model.MemoryItem
		db.First(&item, "id = ?", id)
		if item.AccessCount != 1 {
			t.Fatalf("Expected access_count 1, got %d", item.AccessCount)
		}
		if item.LastAccessAt == nil {
			t.Fatal("Expected last_access_at to be set")
		}
	})

	t.Run("touch non-existent", func(t *testing.T) {
		err := svc.Touch(ctx, "non-existent-id")
		if err == nil {
			t.Fatal("Expected error for non-existent item")
		}
	})
}

func TestMemoryService_Callbacks(t *testing.T) {
	db := store.SetupTestDB(t)
	ctx := context.Background()

	var createdID string
	var updatedItem model.MemoryItem
	var deletedID string

	config := Config{
		OnCreated: func(ctx context.Context, item model.MemoryItem) {
			createdID = item.ID
		},
		OnUpdated: func(ctx context.Context, item model.MemoryItem) {
			updatedItem = item
		},
		OnDeleted: func(ctx context.Context, id string) {
			deletedID = id
		},
	}

	svc := NewMemoryService(db).WithConfig(config)

	t.Run("onCreated callback", func(t *testing.T) {
		id, _ := svc.Remember(ctx, RememberRequest{
			Namespace:     "test/callback",
			NamespaceType: model.NamespaceTypeTransient,
			Content:       "Callback test",
		})

		// Small delay for callback
		time.Sleep(10 * time.Millisecond)
		if createdID != id {
			t.Fatalf("Expected callback ID %s, got %s", id, createdID)
		}
	})

	t.Run("onUpdated callback", func(t *testing.T) {
		var item model.MemoryItem
		db.First(&item, "id = ?", createdID)

		newContent := "Updated"
		svc.Update(ctx, UpdateRequest{
			ItemID:          createdID,
			Content:         &newContent,
			ExpectedVersion: item.Version,
		})

		time.Sleep(10 * time.Millisecond)
		if updatedItem.ID != createdID {
			t.Fatalf("Expected callback for ID %s", createdID)
		}
	})

	t.Run("onDeleted callback", func(t *testing.T) {
		svc.Forget(ctx, ForgetRequest{
			ItemIDs: []string{createdID},
			Mode:    "soft",
		})

		time.Sleep(10 * time.Millisecond)
		if deletedID != createdID {
			t.Fatalf("Expected callback for ID %s, got %s", createdID, deletedID)
		}
	})
}
