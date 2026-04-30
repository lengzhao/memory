package service

import (
	"context"
	"testing"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/store"
)

func TestExtractor_PersistMemoryIsolation(t *testing.T) {
	db := store.SetupTestDB(t)
	extractor := NewExtractor(db)

	mem := ExtractedMemory{
		Namespace:  model.NamespaceTypeTransient,
		Title:      "session fact",
		Content:    "same content",
		Confidence: 0.9,
	}

	ctxS1 := WithIsolation(context.Background(), "t1", "u1", "s1", "planner")
	ctxS2 := WithIsolation(context.Background(), "t1", "u1", "s2", "planner")

	if err := extractor.persistMemory(ctxS1, mem); err != nil {
		t.Fatalf("persist s1 failed: %v", err)
	}
	if err := extractor.persistMemory(ctxS2, mem); err != nil {
		t.Fatalf("persist s2 failed: %v", err)
	}

	var count int64
	if err := db.WithContext(context.Background()).
		Model(&model.MemoryItem{}).
		Where("namespace_type = ? AND content = ?", model.NamespaceTypeTransient, "same content").
		Count(&count).Error; err != nil {
		t.Fatalf("count failed: %v", err)
	}

	if count != 2 {
		t.Fatalf("expected two isolated rows, got %d", count)
	}
}

func TestExtractor_ResolveLLMConfigRequiresModelAndAPIKey(t *testing.T) {
	db := store.SetupTestDB(t)
	extractor := NewExtractor(db)

	_, _, err := extractor.resolveLLMConfig(context.Background(), ExtractRequest{
		LLMConfig: &model.LLMConfig{
			Model: "",
			APIKey: "test-key",
		},
	})
	if err == nil {
		t.Fatal("expected config validation error, got nil")
	}
	if err.Error() != "invalid LLM config: model/api_key are required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

