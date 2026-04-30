// Example: 基于 LLM 的完整记忆提取演示（多轮对话、落库、打印记录）
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/service"
	"github.com/lengzhao/memory/store"
)

func main() {
	ctx := service.WithIsolation(context.Background(), "demo-tenant", "demo-user", "extract-demo-session", "extractor")

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
			LLMConfig:     defaultLLMConfigFromEnv(),
			ReferenceTime: &ref,
			TimeZone:      "Asia/Shanghai",
			// 可选：如 "当前用户：张三。对话中「他」指同事李四。"
			ResolutionContext: "",
		}

		result, err := extractor.Extract(ctx, req)
		if err != nil {
			log.Printf("Extraction failed: %v", err)
			continue
		}

		fmt.Printf("Status: %s (Processing time: %dms)\n", result.Status, result.ProcessingTime)
		fmt.Printf("Tokens: %d\n", result.TotalTokens)

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

func defaultLLMConfigFromEnv() *model.LLMConfig {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Println("⚠️  OPENAI_API_KEY 未设置：提取阶段会失败，请先 export OPENAI_API_KEY=...")
	}
	modelName := os.Getenv("OPENAI_MODEL")
	if modelName == "" {
		modelName = "gpt-5.4-nano"
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
	baseURL := os.Getenv("OPENAI_BASE_URL")
	cfg := &model.LLMConfig{
		APIKey:         apiKey,
		Model:          modelName,
		MaxTokens:      4096,
		Temperature:    temp,
		TimeoutSeconds: timeout,
	}
	if baseURL != "" {
		cfg.BaseURL = &baseURL
	}
	return cfg
}
