package service

import (
	"context"
	"testing"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/store"
)

func TestDecisionEngine_ExecuteAddIsolation(t *testing.T) {
	db := store.SetupTestDB(t)
	de := NewDecisionEngine(db)

	candidate := ExtractedMemory{
		Namespace:  model.NamespaceTypeTransient,
		Title:      "same title",
		Content:    "same content",
		Confidence: 0.9,
	}

	ctxS1 := WithIsolation(context.Background(), "t1", "u1", "s1", "planner")
	ctxS2 := WithIsolation(context.Background(), "t1", "u1", "s2", "planner")

	if _, err := de.executeAdd(ctxS1, candidate); err != nil {
		t.Fatalf("executeAdd s1 failed: %v", err)
	}
	if _, err := de.executeAdd(ctxS2, candidate); err != nil {
		t.Fatalf("executeAdd s2 failed: %v", err)
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

func TestDecisionEngine_FindSimilarMemoriesIsolation(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	de := NewDecisionEngine(db)

	ctxS1 := WithIsolation(context.Background(), "t1", "u1", "s1", "planner")
	ctxS2 := WithIsolation(context.Background(), "t1", "u1", "s2", "planner")

	_, err := svc.Remember(ctxS1, RememberRequest{
		NamespaceType: model.NamespaceTypeTransient,
		Title:         "Go channel notes",
		Content:       "Go channel best practices",
		Tags:          []string{"go", "channel"},
		Confidence:    0.9,
	})
	if err != nil {
		t.Fatalf("remember s1 failed: %v", err)
	}
	_, err = svc.Remember(ctxS2, RememberRequest{
		NamespaceType: model.NamespaceTypeTransient,
		Title:         "Go channel notes",
		Content:       "Go channel best practices",
		Tags:          []string{"go", "channel"},
		Confidence:    0.9,
	})
	if err != nil {
		t.Fatalf("remember s2 failed: %v", err)
	}

	candidate := ExtractedMemory{
		Namespace: model.NamespaceTypeTransient,
		Title:     "Go channel",
		Content:   "channel best practices",
		Tags:      []string{"go", "channel"},
	}

	similar, err := de.FindSimilarMemories(ctxS1, candidate, 10)
	if err != nil {
		t.Fatalf("FindSimilarMemories failed: %v", err)
	}
	if len(similar) == 0 {
		t.Fatal("expected at least one similar memory")
	}

	for _, s := range similar {
		if s.Namespace != "tenant/t1/user/u1/session/s1/agent/planner/transient" {
			t.Fatalf("expected only s1 namespace, got %s", s.Namespace)
		}
	}
}

func TestDecisionEngine_ExecuteUpdateIsolation(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	de := NewDecisionEngine(db)

	ctxS1 := WithIsolation(context.Background(), "t1", "u1", "s1", "planner")
	ctxS2 := WithIsolation(context.Background(), "t1", "u1", "s2", "planner")

	idS1, err := svc.Remember(ctxS1, RememberRequest{
		NamespaceType: model.NamespaceTypeTransient,
		Title:         "s1",
		Content:       "old-s1",
		Confidence:    0.9,
	})
	if err != nil {
		t.Fatalf("remember s1 failed: %v", err)
	}

	candidate := ExtractedMemory{
		Namespace: model.NamespaceTypeTransient,
		Title:     "updated",
		Content:   "new-content",
		Summary:   "new-summary",
		Tags:      []string{"x"},
	}
	decision := MemoryDecision{Decision: DecisionUpdate}

	if err := de.executeUpdate(ctxS2, idS1, candidate, decision); err == nil {
		t.Fatal("expected update to be blocked across isolation boundary")
	}
}

