// Main entry point for the memory server.
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/store"
)

func main() {
	// Initialize database
	cfg := store.DefaultConfig()
	cfg.LogLevel = 4 // Info level for demo

	db, err := store.InitDB(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer store.Close(db)

	// Run GORM auto-migration (creates all tables except FTS5 virtual table)
	fmt.Println("Running GORM AutoMigrate...")
	if err := store.Migrate(db); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	// Execute SQL migration for FTS5 virtual table and triggers
	fmt.Println("Executing FTS5 migration...")
	if err := store.ExecMigrationFile(db, "migrations/001_initial_schema.sql"); err != nil {
		// FTS5 might already exist, continue
		fmt.Printf("Migration file execution (may already exist): %v\n", err)
	}

	// Example: Insert a memory item
	fmt.Println("\nInserting sample memory...")
	now := time.Now()
	item := model.MemoryItem{
		ID:            "01HQ1234567890ABCDEFGHJKLM",
		Namespace:     "transient/demo-001",
		NamespaceType: model.NamespaceTypeTransient,
		Title:         "Demo Memory",
		Content:       "This is a sample memory item for demonstration.",
		Summary:       "Sample memory",
		TagsJSON:      `["demo", "test"]`,
		SourceType:    model.SourceTypeAgent,
		Importance:    50,
		Confidence:    0.95,
		Status:        model.ItemStatusActive,
		CreatedAt:     now,
		UpdatedAt:     now,
		Version:       1,
	}

	if err := db.Create(&item).Error; err != nil {
		log.Printf("Failed to create item (may already exist): %v", err)
	} else {
		fmt.Printf("Created item: %s\n", item.ID)
	}

	// Example: Query memory items
	fmt.Println("\nQuerying memory items...")
	var items []model.MemoryItem
	if err := db.Where("status = ?", model.ItemStatusActive).
		Limit(10).
		Find(&items).Error; err != nil {
		log.Printf("Query failed: %v", err)
	} else {
		fmt.Printf("Found %d active items\n", len(items))
		for _, i := range items {
			fmt.Printf("  - %s: %s (%s)\n", i.ID, i.Title, i.Namespace)
		}
	}

	// Example: FTS5 search (using raw SQL since FTS is virtual table)
	fmt.Println("\nFTS5 search for 'sample':")
	var ftsResults []struct {
		ItemID  string
		Title   string
		Content string
	}
	if err := db.Raw(`
		SELECT m.id as item_id, m.title, m.content
		FROM fts_memory f
		JOIN memory_items m ON m.id = f.item_id
		WHERE fts_memory MATCH ?
		LIMIT 10
	`, "sample").Scan(&ftsResults).Error; err != nil {
		log.Printf("FTS search failed: %v", err)
	} else {
		fmt.Printf("FTS found %d results\n", len(ftsResults))
		for _, r := range ftsResults {
			fmt.Printf("  - %s: %s\n", r.ItemID, r.Title)
		}
	}

	// Example: Insert a policy
	fmt.Println("\nInserting namespace policy...")
	policy := model.NamespacePolicy{
		Namespace:               "session/*",
		TTLSeconds:              intPtr(2592000), // 30 days
		TTLPolicy:               model.TTLPolicySliding,
		SlidingTTLThreshold:     3,
		SummaryEnabled:          true,
		SummaryItemTokenThreshold: 500,
		RankWeightsJSON:         `{"fts":0.55,"recency":0.20,"importance":0.15,"confidence":0.10}`,
		DefaultTopK:             10,
		CreatedAt:               now,
		UpdatedAt:               now,
	}

	if err := db.Create(&policy).Error; err != nil {
		log.Printf("Failed to create policy (may already exist): %v", err)
	} else {
		fmt.Printf("Created policy: %s\n", policy.Namespace)
	}

	fmt.Println("\n✅ Database initialized successfully!")
	fmt.Println("Database file: memory.db")
}

func intPtr(i int) *int {
	return &i
}
