// Example: 基础使用示例
// 展示memory系统的核心功能：记住、回忆、更新、遗忘
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/lengzhao/memory"
)

func main() {
	ctx := context.Background()

	// 配置结构化日志（可选，不配置则使用默认）
	// 示例：使用 JSON 格式输出到 stdout，级别为 Debug
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	memory.SetLogger(slog.New(handler))

	// 1. 初始化数据库 (DefaultConfig 已启用 AutoMigrate)
	fmt.Println("=== 1. 初始化数据库 ===")
	cfg := memory.DefaultConfig()
	cfg.Path = "example.db" // 文件数据库，方便查看

	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)
	fmt.Println("数据库初始化完成")

	// 2. 创建MemoryService
	svc := memory.NewMemoryService(db)

	// 3. 存储记忆 - 用户偏好
	fmt.Println("\n=== 2. 存储用户偏好 ===")
	prefID, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "user/preferences",
		NamespaceType: memory.NamespaceProfile,
		Title:         "喜欢的编程语言",
		Content:       "用户喜欢用Go语言编写后端服务，也喜欢Python进行数据分析",
		Tags:          []string{"preference", "programming", "go", "python"},
		SourceType:    memory.SourceUser,
		Importance:    80,
		Confidence:    0.95,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("存储用户偏好，ID: %s\n", prefID)

	// 4. 存储记忆 - 任务/行动
	fmt.Println("\n=== 3. 存储待办任务 ===")
	taskID, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "user/tasks",
		NamespaceType: memory.NamespaceAction,
		Title:         "完成API文档",
		Content:       "需要在本周五之前完成memory系统的API文档编写",
		Tags:          []string{"task", "documentation", "urgent"},
		SourceType:    memory.SourceAgent,
		Importance:    90,
		Confidence:    1.0,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("存储任务，ID: %s\n", taskID)

	// 5. 存储带TTL的记忆 - 临时上下文
	fmt.Println("\n=== 4. 存储临时上下文 (30秒TTL) ===")
	ttl := 30 // 30秒后过期
	contextID, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "session/current",
		NamespaceType: memory.NamespaceTransient,
		Title:         "当前对话上下文",
		Content:       "用户正在询问关于memory系统的问题",
		Tags:          []string{"context", "conversation"},
		SourceType:    memory.SourceSystem,
		TTLSeconds:    &ttl,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("存储临时上下文，ID: %s (30秒后过期)\n", contextID)

	// 6. 回忆记忆 - 按namespace查询
	fmt.Println("\n=== 5. 回忆用户偏好 ===")
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Namespaces:    []string{"user/preferences"},
		MinConfidence: 0.5,
		TopK:          10,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("找到 %d 条用户偏好:\n", len(hits))
	for _, hit := range hits {
		fmt.Printf("  - %s: %s (重要性: %d)\n", hit.Title, hit.Content, hit.Importance)
	}

	// 7. 全文搜索
	fmt.Println("\n=== 6. 全文搜索 '知识星球文档' ===")
	searchHits, err := svc.Recall(ctx, memory.RecallRequest{
		Query: "知识星球文档",
		TopK:  10,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("搜索 '知识星球文档' 找到 %d 条结果:\n", len(searchHits))
	for _, hit := range searchHits {
		fmt.Printf("  - %s: %s\n", hit.Title, hit.Content)
	}

	// 8. 标签过滤
	fmt.Println("\n=== 7. 按标签过滤 'urgent' ===")
	tagHits, err := svc.Recall(ctx, memory.RecallRequest{
		TagsAny: []string{"urgent"},
		TopK:    10,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("标签 'urgent' 找到 %d 条结果:\n", len(tagHits))
	for _, hit := range tagHits {
		fmt.Printf("  - %s\n", hit.Title)
	}

	// 9. 更新记忆
	fmt.Println("\n=== 8. 更新任务状态 ===")
	newContent := "API文档已完成，等待审核"
	if err := svc.Update(ctx, memory.UpdateRequest{
		ItemID:          taskID,
		Content:         &newContent,
		ExpectedVersion: 1, // 乐观锁：预期版本1
	}); err != nil {
		log.Fatal(err)
	}
	fmt.Println("任务已更新")

	// 10. 触摸记忆（更新访问统计）
	fmt.Println("\n=== 9. 触摸记忆（更新访问统计） ===")
	if err := svc.Touch(ctx, prefID); err != nil {
		log.Fatal(err)
	}
	fmt.Println("已更新访问统计")

	// 11. 检查临时上下文（在过期前）
	fmt.Println("\n=== 10. 检查临时上下文（应该存在） ===")
	transientHits, _ := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"session/current"},
	})
	fmt.Printf("临时上下文: %d 条\n", len(transientHits))

	// 12. 等待TTL过期
	fmt.Println("\n=== 11. 等待30秒让临时上下文过期... ===")
	time.Sleep(31 * time.Second)

	// 13. 再次检查（应该不存在了）
	fmt.Println("=== 12. 再次检查临时上下文（应该已过期） ===")
	transientHits, _ = svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"session/current"},
	})
	fmt.Printf("临时上下文: %d 条 (已过期被排除)\n", len(transientHits))

	// 14. 软删除记忆
	fmt.Println("\n=== 13. 软删除任务 ===")
	count, err := svc.Forget(ctx, memory.ForgetRequest{
		ItemIDs: []string{taskID},
		Mode:    "soft",
		Reason:  "任务已完成归档",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("已删除 %d 条记忆\n", count)

	// 15. 验证删除
	fmt.Println("\n=== 14. 验证删除 ===")
	hits, _ = svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"user/tasks"},
	})
	fmt.Printf("用户任务: %d 条 (已删除的不显示)\n", len(hits))

	fmt.Println("\n=== 示例完成 ===")
}
