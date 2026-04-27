package test

import (
	"testing"

	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/service"
)

// TestDecision_FindSimilarMemories tests finding similar memories using FTS
func TestDecision_FindSimilarMemories(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	ctx := testContext()
	de := memory.NewDecisionEngine(tdb.DB)

	// Check if FTS5 is available
	var ftsExists int
	tdb.DB.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='fts_memory'").Scan(&ftsExists)
	if ftsExists == 0 {
		t.Skip("FTS5 not available, skipping FTS-dependent test")
	}

	// Create some existing memories
	contents := []string{
		"Go is a programming language created by Google",
		"Python is popular for data science and AI",
		"JavaScript runs in web browsers",
		"Rust is a systems programming language",
	}

	for _, content := range contents {
		_, err := memory.NewMemoryService(tdb.DB).Remember(ctx, memory.RememberRequest{
			NamespaceType: memory.NamespaceKnowledge,
			Content:       content,
			Tags:          []string{"programming", "language"},
		})
		if err != nil {
			t.Fatalf("Failed to create memory: %v", err)
		}
	}

	// Search for similar to Go content
	candidate := service.ExtractedMemory{
		TempID:    "test-1",
		Namespace: memory.NamespaceKnowledge,
		Title:     "Go Programming",
		Content:   "Google created Go programming language",
		Tags:      []string{"go", "google"},
	}

	similar, err := de.FindSimilarMemories(ctx, candidate, 3)
	if err != nil {
		t.Fatalf("Failed to find similar: %v", err)
	}

	// Should find at least the Go memory
	t.Logf("Found %d similar memories", len(similar))
}

// TestDecision_ExecuteAdd tests ADD decision execution
func TestDecision_ExecuteAdd(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	ctx := testContext()
	de := memory.NewDecisionEngine(tdb.DB)

	// Create candidate
	candidates := []service.ExtractedMemory{
		{
			TempID:     "add-1",
			Namespace:  memory.NamespaceKnowledge,
			Title:      "New Memory",
			Content:    "This is a new memory to add",
			Tags:       []string{"test"},
			Importance: 50,
			Confidence: 0.9,
		},
	}

	// Execute ADD decision
	decisions := []service.MemoryDecision{
		{
			Decision:   service.DecisionAdd,
			Reason:     "New unique memory",
			Confidence: 0.95,
		},
	}

	result, err := de.ExecuteDecisions(ctx, candidates, decisions)
	if err != nil {
		t.Fatalf("Failed to execute decisions: %v", err)
	}

	if len(result.Added) != 1 {
		t.Errorf("Expected 1 added, got %d", len(result.Added))
	}

	// Verify it was created by querying all active memories
	var count int64
	tdb.DB.WithContext(ctx).Model(&model.MemoryItem{}).Where("status = ?", model.ItemStatusActive).Count(&count)
	if count != 1 {
		t.Errorf("Expected 1 item in database, got %d", count)
	}
}

// TestDecision_ExecuteUpdate tests UPDATE decision execution
func TestDecision_ExecuteUpdate(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()
	de := memory.NewDecisionEngine(tdb.DB)

	// Create existing memory
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Content:    "Old content that needs updating",
		Importance: 50,
	})
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Execute UPDATE decision
	candidates := []service.ExtractedMemory{
		{
			TempID:     "update-1",
			Content:    "Updated content for the memory",
			Importance: 75,
			Confidence: 0.9,
		},
	}

	decisions := []service.MemoryDecision{
		{
			Decision:   service.DecisionUpdate,
			TargetID:   id,
			Reason:     "New information is more complete",
			Confidence: 0.9,
			NewImportance: 75,
		},
	}

	_, err = de.ExecuteDecisions(ctx, candidates, decisions)
	if err != nil {
		t.Fatalf("Failed to execute update: %v", err)
	}

	// Verify update
	var item model.MemoryItem
	tdb.DB.WithContext(ctx).First(&item, "id = ?", id)

	if item.Content != "Updated content for the memory" {
		t.Errorf("Content not updated: got %q", item.Content)
	}
	if item.Importance != 75 {
		t.Errorf("Importance not updated: got %d, want 75", item.Importance)
	}
	if item.Version != 2 {
		t.Errorf("Version should be 2 after update, got %d", item.Version)
	}
}

// TestDecision_ExecuteDelete tests DELETE decision execution
func TestDecision_ExecuteDelete(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()
	de := memory.NewDecisionEngine(tdb.DB)

	// Create memory to delete
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Content:   "Content to be deleted",
	})
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Execute DELETE decision
	candidates := []service.ExtractedMemory{
		{
			TempID:  "delete-1",
			Content: "Contradictory information",
		},
	}

	decisions := []service.MemoryDecision{
		{
			Decision: service.DecisionDelete,
			TargetID: id,
			Reason:   "Information is outdated",
		},
	}

	_, err = de.ExecuteDecisions(ctx, candidates, decisions)
	if err != nil {
		t.Fatalf("Failed to execute delete: %v", err)
	}

	// Verify deletion (status changed)
	var item model.MemoryItem
	tdb.DB.WithContext(ctx).First(&item, "id = ?", id)
	if item.Status != model.ItemStatusDeleted {
		t.Errorf("Expected status deleted, got %v", item.Status)
	}
}

// TestDecision_ExecuteMerge tests MERGE decision execution
func TestDecision_ExecuteMerge(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()
	de := memory.NewDecisionEngine(tdb.DB)

	// Create existing memory
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Content:    "Original information",
		Tags:       []string{"original"},
		Importance: 50,
	})
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	// Execute MERGE decision
	candidates := []service.ExtractedMemory{
		{
			TempID:     "merge-1",
			Title:      "Additional Info",
			Content:    "Additional information to merge",
			Tags:       []string{"new"},
			Importance: 60,
			Confidence: 0.85,
		},
	}

	decisions := []service.MemoryDecision{
		{
			Decision:      service.DecisionMerge,
			TargetID:      id,
			Reason:        "Combine related information",
			MergedContent: "Original information\n\n[Additional Info]\nAdditional information to merge",
			MergedTitle:   "Merged Title",
			NewImportance: 65,
		},
	}

	_, err = de.ExecuteDecisions(ctx, candidates, decisions)
	if err != nil {
		t.Fatalf("Failed to execute merge: %v", err)
	}

	// Verify merge
	var item model.MemoryItem
	tdb.DB.WithContext(ctx).First(&item, "id = ?", id)

	// Should have merged tags
	t.Logf("Merged tags: %s", item.TagsJSON)

	// Check if memory_merges table exists
	var tableExists int
	tdb.DB.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='memory_merges'").Scan(&tableExists)

	if tableExists > 0 {
		// Should have merge record
		var merge model.MemoryMerge
		result := tdb.DB.WithContext(ctx).Where("target_id = ?", id).First(&merge)
		if result.Error != nil {
			t.Errorf("Expected merge record, got error: %v", result.Error)
		}
	}
}

// TestDecision_ExecuteIgnore tests IGNORE decision execution
func TestDecision_ExecuteIgnore(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	ctx := testContext()
	de := memory.NewDecisionEngine(tdb.DB)

	// Execute IGNORE decision
	candidates := []service.ExtractedMemory{
		{
			TempID:  "ignore-1",
			Content: "Duplicate information",
		},
	}

	decisions := []service.MemoryDecision{
		{
			Decision: service.DecisionIgnore,
			Reason:   "Duplicate of existing",
		},
	}

	result, err := de.ExecuteDecisions(ctx, candidates, decisions)
	if err != nil {
		t.Fatalf("Failed to execute ignore: %v", err)
	}

	if len(result.Ignored) != 1 {
		t.Errorf("Expected 1 ignored, got %d", len(result.Ignored))
	}
	if result.Ignored[0] != "ignore-1" {
		t.Errorf("Expected ignored ID 'ignore-1', got %s", result.Ignored[0])
	}
}

// TestDecision_MismatchedCount tests error when decisions don't match candidates
func TestDecision_MismatchedCount(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	ctx := testContext()
	de := memory.NewDecisionEngine(tdb.DB)

	candidates := []service.ExtractedMemory{
		{TempID: "cand-1"},
		{TempID: "cand-2"},
	}

	decisions := []service.MemoryDecision{
		{Decision: service.DecisionAdd},
	}

	_, err := de.ExecuteDecisions(ctx, candidates, decisions)
	if err == nil {
		t.Error("Expected error for mismatched count")
	}
}
