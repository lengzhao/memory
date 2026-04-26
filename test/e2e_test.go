// Package test provides end-to-end tests for the memory system.
// These tests use in-memory SQLite for fast, isolated execution.
package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/store"
	"gorm.io/gorm"
)

// TestMain handles global test setup and teardown
func TestMain(m *testing.M) {
	// Run tests
	code := m.Run()
	os.Exit(code)
}

// setupTestDB creates an in-memory database for testing
func setupTestDB(t *testing.T) *TestDB {
	t.Helper()

	cfg := memory.DefaultConfig()
	cfg.Path = filepath.Join(t.TempDir(), "test.db")

	db, err := memory.InitDB(cfg)
	if err != nil {
		t.Fatalf("Failed to init database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("Failed to get sql DB handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := store.Migrate(db); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	return &TestDB{DB: db}
}

// TestDB wraps a database instance with test helpers
type TestDB struct {
	DB *gorm.DB
}

// Cleanup closes the database
func (tdb *TestDB) Cleanup() {
	if tdb.DB != nil {
		memory.Close(tdb.DB)
	}
}

// context returns a background context for tests
func testContext() context.Context {
	return context.Background()
}

func TestExtractE2E_RealLLM_DryRun(t *testing.T) {
	cfg := loadOpenAIConfigFromDotEnv(t)

	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	extractor := memory.NewExtractor(tdb.DB)
	ctx, cancel := context.WithTimeout(testContext(), 120*time.Second)
	defer cancel()

	dialog := "我叫王小明，我喜欢深色主题，并且下周三要提交季度复盘。"
	result, err := extractor.Extract(ctx, memory.ExtractRequest{
		DialogText:    dialog,
		MinConfidence: 0.7,
		DryRun:        true,
		LLMConfig:     cfg,
		TimeZone:      "Asia/Shanghai",
	})
	if err != nil {
		t.Fatalf("real extract failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil extract result")
	}
	if result.Status != "completed" && result.Status != "cached" {
		t.Fatalf("expected status completed/cached, got %q", result.Status)
	}
	if len(result.Memories) == 0 {
		t.Fatal("expected at least one extracted memory from real LLM")
	}
	for i, mem := range result.Memories {
		if strings.TrimSpace(mem.Title) == "" {
			t.Fatalf("memory[%d] has empty title", i)
		}
		if strings.TrimSpace(mem.Content) == "" {
			t.Fatalf("memory[%d] has empty content", i)
		}
		if mem.Confidence < 0.7 {
			t.Fatalf("memory[%d] confidence %.2f is below min confidence", i, mem.Confidence)
		}
		t.Logf("--------------------------------")
		t.Logf("memory[%d] namespace: %s", i, mem.Namespace)
		t.Logf("memory[%d] title: %s, content: %s, confidence: %.2f", i, mem.Title, mem.Content, mem.Confidence)
		t.Logf("memory[%d] summary: %s", i, mem.Summary)
		t.Logf("memory[%d] tags: %v", i, mem.Tags)
		t.Logf("memory[%d] importance: %d", i, mem.Importance)
		t.Logf("memory[%d] reasoning: %s", i, mem.Reasoning)
		t.Logf("memory[%d] task_metadata: %v", i, mem.TaskMetadata)
	}

	var count int64
	if err := tdb.DB.Model(&model.MemoryItem{}).Count(&count).Error; err != nil {
		t.Fatalf("failed to count memory items: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no persisted memory in dry-run mode, got %d", count)
	}
	t.Logf("count: %+v", count)
}

func TestExtractE2E_RealLLM_CacheHit(t *testing.T) {
	cfg := loadOpenAIConfigFromDotEnv(t)

	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	extractor := memory.NewExtractor(tdb.DB)
	ctx, cancel := context.WithTimeout(testContext(), 120*time.Second)
	defer cancel()

	dialog := "我最近在学习Go并发，尤其是channel的最佳实践。"

	first, err := extractor.Extract(ctx, memory.ExtractRequest{
		DialogText:    dialog,
		MinConfidence: 0.7,
		DryRun:        true,
		LLMConfig:     cfg,
	})
	if err != nil {
		t.Fatalf("first extract failed: %v", err)
	}
	if first.Status != "completed" && first.Status != "cached" {
		t.Fatalf("expected first status completed/cached, got %q", first.Status)
	}

	second, err := extractor.Extract(ctx, memory.ExtractRequest{
		DialogText:    dialog,
		MinConfidence: 0.7,
		DryRun:        true,
		LLMConfig:     cfg,
	})
	if err != nil {
		t.Fatalf("second extract failed: %v", err)
	}
	if second.Status != "cached" {
		t.Fatalf("expected second extract status cached, got %q", second.Status)
	}
	if second.ExtractionID != first.ExtractionID {
		t.Fatalf("expected same extraction id on cache hit, got %q vs %q", first.ExtractionID, second.ExtractionID)
	}
}

func loadOpenAIConfigFromDotEnv(t *testing.T) *model.LLMConfig {
	t.Helper()
	if os.Getenv("RUN_REAL_LLM_TESTS") != "1" {
		t.Skip("real LLM e2e disabled by default; set RUN_REAL_LLM_TESTS=1 to enable")
	}

	dotEnvPath := filepath.Join("..", ".env")
	raw, err := os.ReadFile(dotEnvPath)
	if err != nil {
		t.Skipf("failed to read %s: %v", dotEnvPath, err)
	}

	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, `"'`)
		values[key] = val
	}

	apiKey := values["OPENAI_API_KEY"]
	modelName := values["OPENAI_MODEL"]
	baseURL := values["OPENAI_BASE_URL"]
	if apiKey == "" || modelName == "" || baseURL == "" {
		t.Skip("missing OPENAI_API_KEY/OPENAI_MODEL/OPENAI_BASE_URL in .env for real e2e extract tests")
	}

	timeout := 120
	return &model.LLMConfig{
		Provider:       model.LLMProviderOpenAI,
		APIKey:         apiKey,
		BaseURL:        &baseURL,
		Model:          modelName,
		MaxTokens:      4096,
		Temperature:    1,
		TimeoutSeconds: timeout,
	}
}
