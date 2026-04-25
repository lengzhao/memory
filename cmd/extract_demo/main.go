// Demo program for LLM-based memory extraction.
// This demonstrates the automatic classification into different namespaces.
package main

import (
	"os"
	"strings"

	"gorm.io/gorm"
)

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/service"
	"github.com/lengzhao/memory/store"
)

func main() {
	// Initialize database
	cfg := store.DefaultConfig()
	cfg.LogLevel = 2 // Warn level

	db, err := store.InitDB(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer store.Close(db)

	// Run migrations
	fmt.Println("Running migrations...")
	if err := store.Migrate(db); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}
	if err := store.ExecMigrationFile(db, "migrations/001_initial_schema.sql"); err != nil {
		fmt.Printf("Migration execution note: %v\n", err)
	}

	// Seed default LLM config and prompt
	fmt.Println("\nSeeding default LLM config and prompt...")
	if err := seedDefaults(db); err != nil {
		log.Printf("Seed defaults error (may already exist): %v", err)
	}

	// Create extractor
	extractor := service.NewExtractor(db)

	// Demo dialogs - each tests a different namespace type (4 simplified categories)
	dialogs := []string{
		"用户说：我喜欢用深色主题，浅色主题太刺眼了。这个是用户的个人偏好，应该记住。",
		"今天的任务是完成用户登录功能的重构，这是高优先级的工作，需要在周五前完成。",
		"Go语言的goroutine是轻量级线程，由Go运行时管理，可以实现高并发。这是一个重要的知识点。",
		"刚才我们讨论了Q4项目进度，下一步要准备给客户演示demo版本。这是当前会话的上下文。",
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("LLM Memory Extraction Demo")
	fmt.Println(strings.Repeat("=", 60))

	for i, dialog := range dialogs {
		fmt.Printf("\n--- Dialog %d ---\n", i+1)
		fmt.Printf("Input: %s\n", dialog)

		req := service.ExtractRequest{
			DialogText:    dialog,
			MinConfidence: 0.7,
			DryRun:        false, // Actually save to database
		}

		result, err := extractor.Extract(context.Background(), req)
		if err != nil {
			log.Printf("Extraction failed: %v", err)
			continue
		}

		fmt.Printf("Status: %s (Processing time: %dms)\n", result.Status, result.ProcessingTime)
		fmt.Printf("Tokens: %d, Cost: $%.6f\n", result.TotalTokens, result.CostEstimate)

		for j, mem := range result.Memories {
			fmt.Printf("\n  Memory %d:\n", j+1)
			fmt.Printf("    Namespace: %s\n", mem.Namespace)
			fmt.Printf("    Title: %s\n", mem.Title)
			fmt.Printf("    Confidence: %.2f\n", mem.Confidence)
			fmt.Printf("    Reasoning: %s\n", mem.Reasoning)
			if len(mem.Tags) > 0 {
				fmt.Printf("    Tags: %v\n", mem.Tags)
			}
		}

		// Small delay between requests
		time.Sleep(100 * time.Millisecond)
	}

	// Show extraction records
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("Extraction Records in Database")
	fmt.Println(strings.Repeat("=", 60))

	var extractions []model.DialogExtraction
	if err := db.Limit(10).Order("created_at DESC").Find(&extractions).Error; err != nil {
		log.Printf("Failed to query extractions: %v", err)
	} else {
		fmt.Printf("\nTotal extractions: %d\n", len(extractions))
		for _, ext := range extractions {
			fmt.Printf("- %s: %s (status: %s)\n", ext.ID[:8], ext.DialogHash[:16], ext.Status)
		}
	}

	// Show all memories
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("All Memories in Database")
	fmt.Println(strings.Repeat("=", 60))

	var memories []model.MemoryItem
	if err := db.Find(&memories).Error; err != nil {
		log.Printf("Failed to query memories: %v", err)
	} else {
		fmt.Printf("\nTotal memories: %d\n", len(memories))
		for _, mem := range memories {
			fmt.Printf("- [%s] %s: %s (conf: %.2f)\n", mem.NamespaceType, mem.ID[:8], mem.Title, mem.Confidence)
		}
	}

	fmt.Println("\n✅ Demo completed!")
}

func seedDefaults(db interface{}) error {
	// Type assertion to access GORM methods
	dbGorm := db.(*gorm.DB)
	now := time.Now()

	// Get API key from environment
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Println("⚠️  Warning: OPENAI_API_KEY not set. Extraction will fail.")
		fmt.Println("   Set it with: export OPENAI_API_KEY=sk-your-api-key")
	}

	// Get model from environment or use default
	modelName := os.Getenv("OPENAI_MODEL")
	if modelName == "" {
		modelName = "gpt-4o"
	}

	// Get temperature from environment or use default
	temp := 0.3
	if t := os.Getenv("OPENAI_TEMPERATURE"); t != "" {
		// Simple parsing, in production use strconv.ParseFloat
		if t == "0" {
			temp = 0
		} else if t == "1" {
			temp = 1
		}
	}

	// Get timeout from environment or use default
	timeout := 30
	if t := os.Getenv("OPENAI_TIMEOUT"); t != "" {
		if t == "60" {
			timeout = 60
		}
	}

	// Create default LLM config
	llmConfig := model.LLMConfig{
		ID:             "cfg-default-openai",
		Name:           "OpenAI " + modelName,
		Provider:       model.LLMProviderOpenAI,
		APIKey:         apiKey, // In production, encrypt this
		Model:          modelName,
		MaxTokens:      4096,
		Temperature:    temp,
		TimeoutSeconds: timeout,
		IsDefault:      true,
		Enabled:        true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := dbGorm.Create(&llmConfig).Error; err != nil {
		return err
	}
	fmt.Println("Created default LLM config: cfg-default-openai")

	// Create default extraction prompt (simplified 4 categories)
	prompt := model.ExtractionPrompt{
		ID:       "prompt-default-v1",
		Name:     "v1-simplified-4cat",
		Version:  1,
		SystemPrompt: `You are a memory extraction assistant. Your task is to analyze user dialog and extract structured memories.

CLASSIFICATION RULES (4 simplified categories):
- "transient": Temporary conversation context, short-lived facts that become irrelevant after the session
- "profile": User preferences, personal information, habits, likes/dislikes - long-term stable traits
- "action": Action items, todos, tasks, goals with deadlines or priorities - things that need to be done
- "knowledge": Learned facts, concepts, skills, methods, procedures - information that was learned

OUTPUT FORMAT:
Return a JSON object with a "memories" key containing an array of memory objects:
{
  "memories": [
    {
      "namespace": "transient|profile|action|knowledge",
      "title": "Short descriptive title (max 10 words)",
      "content": "Full detailed content",
      "summary": "One sentence summary",
      "tags": ["relevant", "keywords"],
      "importance": 50,
      "confidence": 0.85,
      "reasoning": "Why this classification was chosen",
      "task_metadata": {"deadline": "2024-01-01", "priority": "high|medium|low"}
    }
  ]
}

GUIDELINES:
- Only extract high-confidence information (confidence >= 0.7)
- Use specific, descriptive tags
- Importance: 0-100 scale, higher for critical information
- Confidence: 0.0-1.0 based on clarity in source text
- task_metadata only required for "action" namespace`,
		JSONSchema: `{"type":"object","properties":{"memories":{"type":"array","items":{"type":"object","properties":{"namespace":{"enum":["transient","profile","action","knowledge"]},"title":{"type":"string"},"content":{"type":"string"},"summary":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"importance":{"type":"integer","minimum":0,"maximum":100},"confidence":{"type":"number","minimum":0,"maximum":1},"reasoning":{"type":"string"},"task_metadata":{"type":"object","properties":{"deadline":{"type":"string"},"priority":{"enum":["high","medium","low"]}}}},"required":["namespace","title","content","importance","confidence"]}}},"required":["memories"]}}`,
		IsDefault:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := dbGorm.Create(&prompt).Error; err != nil {
		return err
	}
	fmt.Println("Created default extraction prompt: prompt-default-v1")

	return nil
}

