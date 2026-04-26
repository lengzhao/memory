package test

import (
	"testing"

	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/model"
)

// TestDBInit_MemoryDB tests in-memory database initialization
func TestDBInit_MemoryDB(t *testing.T) {
	cfg := memory.DefaultConfig()
	cfg.Path = ":memory:"

	db, err := memory.InitDB(cfg)
	if err != nil {
		t.Fatalf("Failed to init database: %v", err)
	}
	defer memory.Close(db)

	if db == nil {
		t.Fatal("Database is nil")
	}
}

// TestDBMigrate_AutoMigrate tests GORM AutoMigrate for all models
func TestDBMigrate_AutoMigrate(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	// Verify tables exist by inserting records
	ctx := testContext()

	// Insert a memory item
	item := model.MemoryItem{
		ID:        model.GenerateID(),
		Namespace: "test/migrate",
		Content:   "Test content",
		Status:    model.ItemStatusActive,
	}
	if err := tdb.DB.WithContext(ctx).Create(&item).Error; err != nil {
		t.Fatalf("Failed to create memory item: %v", err)
	}

	// Verify retrieval
	var retrieved model.MemoryItem
	if err := tdb.DB.WithContext(ctx).First(&retrieved, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("Failed to retrieve memory item: %v", err)
	}

	if retrieved.Content != item.Content {
		t.Errorf("Content mismatch: got %q, want %q", retrieved.Content, item.Content)
	}
}

// TestDBMigrate_LLMTables tests LLM-related tables exist
func TestDBMigrate_LLMTables(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	ctx := testContext()

	// Test LLMConfig
	config := model.LLMConfig{
		ID:       model.GenerateID(),
		Name:     "test-config",
		Provider: model.LLMProviderOpenAI,
		Model:    "gpt-4o",
	}
	if err := tdb.DB.WithContext(ctx).Create(&config).Error; err != nil {
		t.Fatalf("Failed to create LLM config: %v", err)
	}

	// Test ExtractionPrompt
	prompt := model.ExtractionPrompt{
		ID:           model.GenerateID(),
		Name:         "test-prompt",
		SystemPrompt: "You are a test",
		JSONSchema:   "{}",
	}
	if err := tdb.DB.WithContext(ctx).Create(&prompt).Error; err != nil {
		t.Fatalf("Failed to create extraction prompt: %v", err)
	}

	// Test DialogExtraction
	extraction := model.DialogExtraction{
		ID:          model.GenerateID(),
		DialogText:  "Test dialog",
		DialogHash:  "abc123",
		LLMConfigID: config.ID,
		PromptID:    prompt.ID,
		Status:      model.ExtractionStatusCompleted,
	}
	if err := tdb.DB.WithContext(ctx).Create(&extraction).Error; err != nil {
		t.Fatalf("Failed to create dialog extraction: %v", err)
	}
}

// TestDBMigrate_MemoryMergeTable tests memory_merge table exists
func TestDBMigrate_MemoryMergeTable(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	var tableExists int
	tdb.DB.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='memory_merges'").Scan(&tableExists)
	if tableExists == 0 {
		t.Fatalf("memory_merges table missing after Migrate")
	}

	ctx := testContext()

	merge := model.MemoryMerge{
		ID:            model.GenerateID(),
		TargetID:      model.GenerateID(),
		SourceContent: "Source content",
		MergedContent: "Merged content",
	}
	if err := tdb.DB.WithContext(ctx).Create(&merge).Error; err != nil {
		t.Fatalf("Failed to create memory merge record: %v", err)
	}

	var retrieved model.MemoryMerge
	if err := tdb.DB.WithContext(ctx).First(&retrieved, "id = ?", merge.ID).Error; err != nil {
		t.Fatalf("Failed to retrieve memory merge: %v", err)
	}
}
