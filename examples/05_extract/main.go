// Example: LLM记忆提取示例
// 展示如何使用Extractor从对话中提取记忆
//
// 运行前请设置环境变量:
//   export OPENAI_API_KEY=sk-your-api-key
//
// 或使用本地模型(Ollama):
//   export OPENAI_BASE_URL=http://localhost:11434/v1
//   export OPENAI_MODEL=llama3.1:8b
//   export OPENAI_API_KEY=ollama
//
// 本示例展示代码直接配置LLM（无需预先存储到DB）
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/model"
)

func main() {
	ctx := context.Background()
	ctx = memory.WithIsolation(ctx, "demo-tenant", "demo-user", "extract-session", "extractor")

	// 初始化数据库（DefaultConfig已启用AutoMigrate）
	cfg := memory.DefaultConfig()
	cfg.Path = "extract_example.db"

	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	// 检查API密钥
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Println("提示: 设置 OPENAI_API_KEY 环境变量以运行实际提取")
		fmt.Println("      或使用本地模型: export OPENAI_BASE_URL=http://localhost:11434/v1")
		apiKey = "placeholder"
	}

	// 配置LLM（代码直接配置，无需存储到DB）
	// 这是新的简化方式：配置通过 ExtractRequest 直接传递
	llmConfig := &model.LLMConfig{
		Provider: model.LLMProviderOpenAI,
		APIKey:   apiKey,
		Model:    getEnv("OPENAI_MODEL", "gpt-4o"),
		BaseURL:  strPtr(getEnv("OPENAI_BASE_URL", "")),
		// 代码配置方式：无需 IsDefault，也无需保存到DB
	}

	// 创建提取器
	extractor := memory.NewExtractor(db)

	// 示例对话
	dialog := `用户: 我喜欢用Go语言写后端程序。
Agent: 好的，我记下了您的技术偏好。
用户: 提醒我明天下午3点开会。
Agent: 已为您设置明天下午3点的会议提醒。`

	fmt.Println("=== LLM记忆提取示例（代码配置方式） ===")
	fmt.Println("\n对话内容:")
	fmt.Println(dialog)
	fmt.Println("\n配置方式: 代码直接传递 LLM 配置，无需 DB 预先存储")

	// 执行提取 - 直接传入配置
	req := memory.ExtractRequest{
		DialogText:    dialog,
		LLMConfig:     llmConfig, // 直接传入配置，无需 DB 查询
		MinConfidence: 0.7,
		// DryRun: true,  // 取消注释以预览而不保存
	}

	fmt.Println("\n执行提取...")
	result, err := extractor.Extract(ctx, req)
	if err != nil {
		log.Printf("提取失败（需要配置API密钥）: %v", err)
		fmt.Println("\n提示: 配置API密钥后重新运行")
		return
	}

	fmt.Printf("\n提取完成: %s (耗时 %dms, %d tokens)\n",
		result.Status, result.ProcessingTime, result.TotalTokens)

	fmt.Printf("\n提取到 %d 条记忆:\n", len(result.Memories))
	for i, mem := range result.Memories {
		fmt.Printf("%d. [%s] %s (置信度: %.2f)\n",
			i+1, mem.Namespace, mem.Title, mem.Confidence)
		fmt.Printf("   内容: %s\n", mem.Content)
	}
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
