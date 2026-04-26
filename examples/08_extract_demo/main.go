// Example: 基于 LLM 的完整记忆提取演示（多轮对话、落库、打印记录）
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/service"
	"github.com/lengzhao/memory/store"
)

func main() {
	cfg := store.DefaultConfig()
	cfg.LogLevel = 2 // Warn level

	db, err := store.InitDB(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer store.Close(db)

	fmt.Println("Running migrations (AutoMigrate + FTS5)...")
	if err := store.Migrate(db); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	// 默认确保库内存在 is_default 的 LLM 与系统提取 Prompt，无需手配即可跑通示例。
	// 若自行维护配置与提示词，可设 EXTRACT_DEMO_NO_SEED=1 跳过。
	if os.Getenv("EXTRACT_DEMO_NO_SEED") == "1" {
		fmt.Println("\nEXTRACT_DEMO_NO_SEED=1：已跳过内置默认项写入。请保证 llm_configs / extraction_prompts 中有默认项。")
	} else {
		if err := ensureDefaultLLMAndPrompt(db); err != nil {
			log.Fatalf("ensure defaults: %v", err)
		}
	}

	extractor := service.NewExtractor(db)

	dialogs := []string{
		"用户说：我喜欢用深色主题，浅色主题太刺眼了。这个是用户的个人偏好，应该记住。",
		"今天的任务是完成用户登录功能的重构，这是高优先级的工作，需要在周五前完成。",
		"Go语言的goroutine是轻量级线程，由Go运行时管理，可以实现高并发。这是一个重要的知识点。",
		"刚才我们讨论了Q4项目进度，下一步要准备给客户演示demo版本。这是当前会话的上下文。",
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("LLM Memory Extraction Demo")
	fmt.Println(strings.Repeat("=", 60))

	// 参考时刻与时区：把「明天」等相对时间解析为具体日期写入记忆；ResolutionContext 用于名字/指代消解
	ref := time.Now()

	for i, dialog := range dialogs {
		fmt.Printf("\n--- Dialog %d ---\n", i+1)
		fmt.Printf("Input: %s\n", dialog)

		req := service.ExtractRequest{
			DialogText:    dialog,
			MinConfidence: 0.7,
			DryRun:        false,
			ReferenceTime: &ref,
			TimeZone:      "Asia/Shanghai",
			// 可选：如 "当前用户：张三。对话中「他」指同事李四。"
			ResolutionContext: "",
		}

		result, err := extractor.Extract(context.Background(), req)
		if err != nil {
			log.Printf("Extraction failed: %v", err)
			continue
		}

		// Calculate cost locally (GPT-4o: ~$0.01 per 1K tokens)
		costEstimate := float64(result.TotalTokens) * 0.000005
		fmt.Printf("Status: %s (Processing time: %dms)\n", result.Status, result.ProcessingTime)
		fmt.Printf("Tokens: %d, Cost: $%.6f\n", result.TotalTokens, costEstimate)

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

		time.Sleep(100 * time.Millisecond)
	}

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

const defaultLLMID = "cfg-default-openai"

// ensureDefaultLLMAndPrompt inserts the demo LLM row and system prompt when missing.
// Custom deployments: set EXTRACT_DEMO_NO_SEED=1 and provide your own rows, or change IDs / DB content as needed.
func ensureDefaultLLMAndPrompt(db *gorm.DB) error {
	var existingCfg model.LLMConfig
	if err := db.Where("id = ?", defaultLLMID).First(&existingCfg).Error; err == nil {
		fmt.Println("Default LLM config already present (" + defaultLLMID + "). Skip insert.")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	} else {
		now := time.Now()
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			fmt.Println("⚠️  OPENAI_API_KEY 未设置：提取阶段会失败，请先 export OPENAI_API_KEY=...")
		}
		modelName := os.Getenv("OPENAI_MODEL")
		if modelName == "" {
			modelName = "gpt-4o"
		}
		temp := 0.3
		if s := os.Getenv("OPENAI_TEMPERATURE"); s != "" {
			if v, e := strconv.ParseFloat(s, 64); e == nil {
				temp = v
			}
		}
		timeout := 30
		if s := os.Getenv("OPENAI_TIMEOUT"); s != "" {
			if v, e := strconv.Atoi(s); e == nil && v > 0 {
				timeout = v
			}
		}
		llmConfig := model.LLMConfig{
			ID:             defaultLLMID,
			Name:           "OpenAI " + modelName,
			Provider:       model.LLMProviderOpenAI,
			APIKey:         apiKey,
			Model:          modelName,
			MaxTokens:      4096,
			Temperature:    temp,
			TimeoutSeconds: timeout,
			IsDefault:      true,
			Enabled:        true,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := db.Create(&llmConfig).Error; err != nil {
			return err
		}
		fmt.Println("Created default LLM config:", defaultLLMID)
	}

	return nil
}
