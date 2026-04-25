// Package memory_test provides usage examples for the memory package.
package memory_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/lengzhao/memory"
)

// Example demonstrates basic usage of the memory package.
func Example() {
	// Initialize database
	cfg := memory.DefaultConfig()
	cfg.Path = "example.db"

	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	// Run migrations
	if err := memory.Migrate(db); err != nil {
		log.Fatal(err)
	}

	// Create a memory item
	item := memory.MemoryItem{
		ID:            memory.GenerateID(),
		Namespace:     "transient/demo",
		NamespaceType: memory.NamespaceTransient,
		Title:         "Demo Session",
		Content:       "This is a demo memory item.",
		Summary:       "Demo session memory",
		TagsJSON:      `["demo", "test"]`,
		SourceType:    memory.SourceAgent,
		Importance:    50,
		Confidence:    0.95,
		Status:        memory.StatusActive,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		Version:       1,
	}

	if err := db.Create(&item).Error; err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Created item with ID length: %d\n", len(item.ID))
	// Output: Created item with ID length: 26
}

// Example_extractor demonstrates automatic memory extraction from dialog.
func Example_extractor() {
	// Initialize database
	db, err := memory.InitDB(memory.DefaultConfig())
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	// Seed default LLM config and prompt (required for extraction)
	// In production, these would be configured by the user

	// Create extractor
	extractor := memory.NewExtractor(db)

	// Extract memories from dialog
	req := memory.ExtractRequest{
		DialogText:    "我喜欢用深色主题，浅色主题太刺眼了",
		MinConfidence: 0.7,
		DryRun:        true, // Preview only, don't save
	}

	result, err := extractor.Extract(context.Background(), req)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Status: %s\n", result.Status)
	fmt.Printf("Found %d memories\n", len(result.Memories))

	for _, mem := range result.Memories {
		fmt.Printf("- [%s] %s (confidence: %.2f)\n", mem.Namespace, mem.Title, mem.Confidence)
	}
}

// Example_namespace shows available namespace types (simplified 4 categories).
func Example_namespace() {
	// These are the 4 simplified namespace types for organizing memories:
	_ = memory.NamespaceTransient // Temporary conversation context
	_ = memory.NamespaceProfile   // User preferences and profile
	_ = memory.NamespaceAction    // Tasks and actionable items
	_ = memory.NamespaceKnowledge // Knowledge, skills, and procedures
}

// Example_ttl shows available TTL policies.
func Example_ttl() {
	// Fixed: Expires after a fixed duration from creation
	_ = memory.TTLFixed

	// Sliding: Expires after a fixed duration from last access
	_ = memory.TTLSliding

	// Manual: Never expires unless explicitly deleted
	_ = memory.TTLManual
}

// Example_providers shows supported LLM providers.
func Example_providers() {
	_ = memory.ProviderOpenAI // OpenAI (GPT-4, GPT-3.5)
	_ = memory.ProviderClaude // Anthropic Claude
	_ = memory.ProviderOllama // Local Ollama models
	_ = memory.ProviderCustom // Custom API endpoint
}
