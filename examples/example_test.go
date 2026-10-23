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

// Example demonstrates storing a memory row directly through the store layer.
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

// Example_memoryService demonstrates the everyday remember/recall path.
func Example_memoryService() {
	cfg := memory.DefaultConfig()
	cfg.Path = ":memory:"
	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	// A scope is the isolation identity: every call states who it acts for.
	scope := memory.MustScope("t1", "u1", "s1", "assistant")
	svc := memory.NewMemoryService(db)
	ctx := context.Background()

	id, err := svc.Remember(ctx, scope, memory.RememberRequest{
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
	fmt.Printf("Stored memory id length: %d\n", len(id))

	hits, err := svc.Recall(ctx, scope, memory.RecallRequest{
		NamespaceTypes: []memory.NamespaceType{memory.NamespaceProfile},
		TopK:           10,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Found %d memories\n", len(hits))
	// Output: Stored memory id length: 26
	// Found 1 memories
}

// Example_memoryService_scopeIsolation shows that memories do not cross sessions.
func Example_memoryService_scopeIsolation() {
	cfg := memory.DefaultConfig()
	cfg.Path = ":memory:"
	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	svc := memory.NewMemoryService(db)
	ctx := context.Background()

	// Same user, different sessions: transient memories stay separate.
	session1 := memory.MustScope("t1", "u1", "s1", "planner")
	session2 := memory.MustScope("t1", "u1", "s2", "planner")

	for scope, content := range map[memory.Scope]string{
		session1: "only-in-session-1",
		session2: "only-in-session-2",
	} {
		if _, err := svc.Remember(ctx, scope, memory.RememberRequest{
			NamespaceType: memory.NamespaceTransient,
			Content:       content,
			SourceType:    memory.SourceUser,
		}); err != nil {
			log.Fatal(err)
		}
	}

	count := func(scope memory.Scope) int {
		hits, err := svc.Recall(ctx, scope, memory.RecallRequest{
			NamespaceTypes: []memory.NamespaceType{memory.NamespaceTransient},
			TopK:           10,
		})
		if err != nil {
			log.Fatal(err)
		}
		return len(hits)
	}

	// Profile memories, by contrast, are shared across the user's sessions.
	if _, err := svc.Remember(ctx, session1, memory.RememberRequest{
		NamespaceType: memory.NamespaceProfile,
		Content:       "prefers dark theme",
		Confidence:    0.9,
	}); err != nil {
		log.Fatal(err)
	}
	profileHits, err := svc.Recall(ctx, session2, memory.RecallRequest{
		NamespaceTypes: []memory.NamespaceType{memory.NamespaceProfile},
		TopK:           10,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("session1 transient=%d, session2 transient=%d, profile visible from session2=%d\n",
		count(session1), count(session2), len(profileHits))
	// Output: session1 transient=1, session2 transient=1, profile visible from session2=1
}

// Example_globalScope shows single-user deployments, which use the global namespaces.
func Example_globalScope() {
	cfg := memory.DefaultConfig()
	cfg.Path = ":memory:"
	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	svc := memory.NewMemoryService(db)
	scope := memory.GlobalScope()

	if _, err := svc.Remember(context.Background(), scope, memory.RememberRequest{
		NamespaceType: memory.NamespaceKnowledge,
		Content:       "SQLite uses WAL for concurrent readers",
		Confidence:    0.9,
	}); err != nil {
		log.Fatal(err)
	}

	var item memory.MemoryItem
	if err := db.First(&item).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("namespace: %s\n", item.Namespace)
	// Output: namespace: knowledge/default
}

// Example_remember_dedupeKey shows idempotent writes.
func Example_remember_dedupeKey() {
	cfg := memory.DefaultConfig()
	cfg.Path = ":memory:"
	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	svc := memory.NewMemoryService(db)
	scope := memory.MustScope("t1", "u1", "s1", "importer")
	ctx := context.Background()

	key := "issue-1234"
	req := memory.RememberRequest{
		NamespaceType: memory.NamespaceAction,
		Content:       "Follow up on issue 1234",
		Confidence:    0.9,
		DedupeKey:     &key,
	}

	first, err := svc.Remember(ctx, scope, req)
	if err != nil {
		log.Fatal(err)
	}
	second, err := svc.Remember(ctx, scope, req)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("same memory: %v\n", first == second)
	// Output: same memory: true
}

// Example_extractor demonstrates automatic extraction. DryRun previews without writing.
func Example_extractor() {
	db, err := memory.InitDB(memory.DefaultConfig())
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	extractor := memory.NewExtractor(db)
	scope := memory.MustScope("t1", "u1", "s1", "extractor")

	result, err := extractor.Extract(context.Background(), scope, memory.ExtractRequest{
		DialogText:    "我喜欢用深色主题，浅色主题太刺眼了",
		MinConfidence: 0.7,
		DryRun:        true,
		LLMConfig:     &memory.LLMConfig{APIKey: "sk-...", Model: "gpt-4o-mini"},
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Status: %s\n", result.Status)
	for _, mem := range result.Memories {
		fmt.Printf("- [%s] %s (confidence: %.2f)\n", mem.Namespace, mem.Title, mem.Confidence)
	}
}
