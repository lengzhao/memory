// Package memory_test provides usage examples for the memory package.
// These examples appear in the Go documentation (pkg.go.dev).
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
	// Initialize database (DefaultConfig has AutoMigrate enabled)
	cfg := memory.DefaultConfig()
	cfg.Path = "example.db"

	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

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

// Example_memoryService demonstrates using MemoryService for common operations.
func Example_memoryService() {
	db, err := memory.InitDB(memory.DefaultConfig())
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	// Create service
	svc := memory.NewMemoryService(db)
	ctx := context.Background()

	// Store a memory
	id, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "user/preferences",
		NamespaceType: memory.NamespaceProfile,
		Title:         "Theme Preference",
		Content:       "User prefers dark theme",
		SourceType:    memory.SourceAgent,
		Importance:    80,
		Confidence:    0.95,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Stored memory: %s...\n", id[:8])

	// Recall memories
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"user/preferences"},
		TopK:       10,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Found %d memories\n", len(hits))
}

// Example_extractor demonstrates automatic memory extraction from dialog.
func Example_extractor() {
	db, err := memory.InitDB(memory.DefaultConfig())
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	// Create extractor
	extractor := memory.NewExtractor(db)

	// Extract memories from dialog (DryRun = preview only)
	req := memory.ExtractRequest{
		DialogText:    "我喜欢用深色主题，浅色主题太刺眼了",
		MinConfidence: 0.7,
		DryRun:        true,
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
