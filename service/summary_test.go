// Package service provides tests for SummaryGenerator.
package service

import (
	"context"
	"testing"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/store"
)

func TestSummaryGenerator_GenerateItemSummary(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	sum := NewSummaryGenerator(db)
	ctx := context.Background()

	t.Run("generate summary for item", func(t *testing.T) {
		// Create item with long content
		content := "This is a very long content that needs to be summarized. " +
			"It contains many words and describes something important about Go programming. " +
			"The summary should capture the key points of this content."

		id, _ := svc.Remember(ctx, RememberRequest{
			Namespace:     "test/summary",
			NamespaceType: model.NamespaceTypeKnowledge,
			Content:       content,
		})

		err := sum.GenerateItemSummary(ctx, id)
		if err != nil {
			t.Fatalf("GenerateItemSummary failed: %v", err)
		}

		// Verify summary was set
		var item model.MemoryItem
		db.First(&item, "id = ?", id)
		if item.Summary == "" {
			t.Fatal("Expected summary to be set")
		}
		if len(item.Summary) > 100 {
			t.Fatalf("Expected summary <= 100 chars, got %d", len(item.Summary))
		}
	})

	t.Run("non-existent item", func(t *testing.T) {
		err := sum.GenerateItemSummary(ctx, "non-existent")
		if err == nil {
			t.Fatal("Expected error for non-existent item")
		}
	})
}

func TestSummaryGenerator_GenerateNamespaceSummary(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	sum := NewSummaryGenerator(db)
	ctx := context.Background()

	t.Run("generate namespace summary", func(t *testing.T) {
		// Create multiple items
		for i := 0; i < 3; i++ {
			svc.Remember(ctx, RememberRequest{
				Namespace:     "test/ns-summary",
				NamespaceType: model.NamespaceTypeKnowledge,
				Title:         "Item " + string(rune('A'+i)),
				Content:       "Content " + string(rune('1'+i)),
			})
		}

		summary, err := sum.GenerateNamespaceSummary(ctx, "test/ns-summary")
		if err != nil {
			t.Fatalf("GenerateNamespaceSummary failed: %v", err)
		}
		if summary == "" {
			t.Fatal("Expected non-empty summary")
		}

		// Verify stored in database
		var ns model.NamespaceSummary
		db.First(&ns, "namespace = ?", "test/ns-summary")
		if ns.Summary == "" {
			t.Fatal("Expected namespace summary to be stored")
		}
		if ns.ItemCount != 3 {
			t.Fatalf("Expected item count 3, got %d", ns.ItemCount)
		}
	})

	t.Run("update existing summary", func(t *testing.T) {
		// Add more items
		svc.Remember(ctx, RememberRequest{
			Namespace:     "test/ns-summary",
			NamespaceType: model.NamespaceTypeKnowledge,
			Title:         "Item D",
			Content:       "Content D",
		})

		summary, err := sum.GenerateNamespaceSummary(ctx, "test/ns-summary")
		if err != nil {
			t.Fatalf("GenerateNamespaceSummary failed: %v", err)
		}
		if summary == "" {
			t.Fatal("Expected non-empty summary")
		}

		// Verify count updated
		var ns model.NamespaceSummary
		db.First(&ns, "namespace = ?", "test/ns-summary")
		if ns.ItemCount != 4 {
			t.Fatalf("Expected updated count 4, got %d", ns.ItemCount)
		}
	})

	t.Run("empty namespace", func(t *testing.T) {
		_, err := sum.GenerateNamespaceSummary(ctx, "test/empty")
		if err == nil {
			t.Fatal("Expected error for empty namespace")
		}
	})
}

func TestGenerateSummary(t *testing.T) {
	tests := []struct {
		content string
		maxLen  int
		wantLen int
	}{
		{"short", 100, 5},
		{"This is a longer text that should be truncated because it exceeds the maximum length limit", 20, 20},
	}

	for _, tt := range tests {
		got := generateSummary(tt.content, tt.maxLen)
		if len(got) != tt.wantLen && len(tt.content) > tt.maxLen {
			t.Errorf("generateSummary() length = %d, want %d", len(got), tt.wantLen)
		}
		if tt.maxLen >= len(tt.content) && got != tt.content {
			t.Errorf("generateSummary() = %v, want %v", got, tt.content)
		}
	}
}
