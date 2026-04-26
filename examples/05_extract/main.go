// Example: LLM记忆提取示例
// 展示如何使用Extractor从对话中提取记忆
// 注意: 需要配置OpenAI API密钥才能运行
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/model"
)

func main() {
	ctx := context.Background()

	// 初始化
	cfg := memory.DefaultConfig()
	cfg.Path = "extract_example.db"

	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	memory.Migrate(db)

	fmt.Println("=== LLM记忆提取示例 ===")

	// 1. 配置LLM（使用占位配置，实际需要API密钥）
	fmt.Println("1. 配置LLM...")

	// 注意: 以下配置是示例，实际使用需要真实的API密钥
	llmConfig := model.LLMConfig{
		ID:        model.GenerateID(),
		Name:      "default",
		Provider:  model.LLMProviderOpenAI,
		APIKey:    "your-api-key-here", // 替换为真实密钥
		Model:     "gpt-4o-mini",
		IsDefault: true,
	}

	// 保存配置到数据库
	if err := db.Create(&llmConfig).Error; err != nil {
		// 如果已存在默认配置，则跳过
		fmt.Println("   LLM配置已存在或跳过")
	} else {
		fmt.Printf("   创建LLM配置: %s\n", llmConfig.ID)
	}

	// 2. 配置提取提示模板
	fmt.Println("\n2. 配置提取提示模板...")
	prompt := model.ExtractionPrompt{
		ID:   model.GenerateID(),
		Name: "default",
		SystemPrompt: `你是一个记忆提取助手。从对话中提取有价值的信息，分类为:
- transient: 临时上下文
- profile: 用户偏好和个人信息
- action: 任务和行动项
- knowledge: 知识点和事实

以JSON格式返回提取的记忆。在 title/content/summary 中须将「明天」等相对时间写为可检索的具体日期，并尽量将「他/她」等解消为专名。`,
		JSONSchema: `{"type":"object","properties":{"memories":{"type":"array","items":{"type":"object","properties":{"namespace":{"type":"string","enum":["transient","profile","action","knowledge"]},"title":{"type":"string"},"content":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"importance":{"type":"integer","minimum":0,"maximum":100},"confidence":{"type":"number","minimum":0,"maximum":1}},"required":["namespace","title","content","importance","confidence"]}}}}`,
		// 不占用 is_default，保留由 Extract 在「无 is_default 行」时使用内建英文默认
		IsDefault: false,
	}

	if err := db.Create(&prompt).Error; err != nil {
		fmt.Println("   提示模板已存在或跳过")
	} else {
		fmt.Printf("   创建提示模板: %s\n", prompt.ID)
	}

	// 3. 创建提取器
	extractor := memory.NewExtractor(db)

	// 4. 示例对话（实际提取需要真实API密钥）
	fmt.Println("\n3. 准备对话提取...")
	dialog := `
用户: 我喜欢用Go语言写后端程序，Python做数据分析。
Agent: 好的，我记下了您的技术偏好。
用户: 对了，提醒我明天下午3点开会。
Agent: 已为您设置明天下午3点的会议提醒。
用户: Redis适合用作缓存，因为它速度快。
Agent: 是的，Redis是内存数据库，读写性能很好。
`

	fmt.Println("   对话内容:")
	fmt.Println(dialog)

	// 5. 提取记忆（演示）
	fmt.Println("\n4. 执行提取（演示模式）...")
	fmt.Println("   注意: 需要真实API密钥才能执行")

	// 示例请求结构
	req := memory.ExtractRequest{
		DialogText:    dialog,
		MinConfidence: 0.7,
		DryRun:        true, // 干运行模式：不实际调用API
	}

	fmt.Printf("   请求配置:\n")
	fmt.Printf("   - 对话长度: %d 字符\n", len(dialog))
	fmt.Printf("   - 最小置信度: %.2f\n", req.MinConfidence)
	fmt.Printf("   - 干运行模式: %v\n", req.DryRun)

	// 如果使用真实API，代码会是:
	// result, err := extractor.Extract(ctx, req)
	// if err != nil {
	//     log.Fatal(err)
	// }
	// for _, mem := range result.Memories {
	//     fmt.Printf("提取到: [%s] %s (置信度: %.2f)\n", mem.Namespace, mem.Title, mem.Confidence)
	// }

	// 避免未使用变量错误
	_ = extractor

	// 6. 决策引擎演示
	fmt.Println("\n5. 决策引擎配置...")
	fmt.Println(`   当 UseDecisionEngine: true 时，提取流程:
   1. LLM提取候选记忆
   2. 查找相似现有记忆（FTS5 BM25）
   3. LLM决策: ADD/UPDATE/DELETE/MERGE/IGNORE
   4. 执行决策并持久化`)

	// 7. 手动添加一些模拟提取的记忆
	fmt.Println("\n6. 手动模拟提取的记忆...")
	svc := memory.NewMemoryService(db)

	// 模拟从对话中提取的用户偏好
	svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "user/profile/auto",
		NamespaceType: memory.NamespaceProfile,
		Title:         "技术栈偏好",
		Content:       "用户喜欢用Go语言编写后端，Python进行数据分析",
		Tags:          []string{"preference", "go", "python"},
		SourceType:    memory.SourceAgent,
		Importance:    75,
		Confidence:    0.9,
	})

	// 模拟提取的任务
	svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "user/tasks/auto",
		NamespaceType: memory.NamespaceAction,
		Title:         "会议提醒",
		Content:       "明天下午3点参加会议",
		Tags:          []string{"task", "meeting", "reminder"},
		SourceType:    memory.SourceAgent,
		Importance:    85,
		Confidence:    0.95,
	})

	// 模拟提取的知识点
	svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "knowledge/cache/auto",
		NamespaceType: memory.NamespaceKnowledge,
		Title:         "Redis缓存",
		Content:       "Redis适合用作缓存，因为它速度快（内存数据库）",
		Tags:          []string{"redis", "cache", "performance"},
		SourceType:    memory.SourceAgent,
		Importance:    60,
		Confidence:    0.85,
	})

	fmt.Println("   已手动添加3条模拟提取的记忆")

	// 8. 查看提取结果
	fmt.Println("\n7. 查看提取的记忆...")
	hits, _ := svc.Recall(ctx, memory.RecallRequest{
		TopK: 10,
	})

	for _, hit := range hits {
		fmt.Printf("   [%s] %s: %s (置信度: %.2f)\n",
			hit.NamespaceType, hit.Title, hit.Content[:30], hit.Confidence)
	}

	fmt.Println("\n=== 示例完成 ===")
	fmt.Println("\n使用步骤:")
	fmt.Println("1. 设置OPENAI_API_KEY环境变量")
	fmt.Println("2. 配置LLMConfig和ExtractionPrompt")
	fmt.Println("3. 创建Extractor并调用Extract方法")
	fmt.Println("4. 可选：启用UseDecisionEngine进行智能合并")
}
