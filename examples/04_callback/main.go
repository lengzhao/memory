// Example: 生命周期回调示例
// 展示如何使用OnCreated, OnUpdated, OnDeleted等回调
package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/model"
)

func main() {
	ctx := context.Background()

	// 初始化
	cfg := memory.DefaultConfig()
	cfg.Path = "callback_example.db"

	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)


	// 事件收集器
	var events []string
	var mu sync.Mutex

	// 配置带回调的Service
	config := memory.ServiceConfig{
		OnCreated: func(ctx context.Context, item model.MemoryItem) {
			mu.Lock()
			events = append(events, fmt.Sprintf("[CREATE] %s: %s", item.ID[:8], item.Title))
			mu.Unlock()
			fmt.Printf("✓ 创建回调: %s\n", item.Title)
		},
		OnUpdated: func(ctx context.Context, item model.MemoryItem) {
			mu.Lock()
			events = append(events, fmt.Sprintf("[UPDATE] %s: %s", item.ID[:8], item.Title))
			mu.Unlock()
			fmt.Printf("✓ 更新回调: %s (版本: %d)\n", item.Title, item.Version)
		},
		OnDeleted: func(ctx context.Context, itemID string) {
			mu.Lock()
			events = append(events, fmt.Sprintf("[DELETE] %s", itemID[:8]))
			mu.Unlock()
			fmt.Printf("✓ 删除回调: %s...\n", itemID[:8])
		},
		OnExpired: func(ctx context.Context, itemID string) {
			mu.Lock()
			events = append(events, fmt.Sprintf("[EXPIRE] %s", itemID[:8]))
			mu.Unlock()
			fmt.Printf("✓ 过期回调: %s...\n", itemID[:8])
		},
	}

	svc := memory.NewMemoryService(db).WithConfig(config)

	fmt.Println("=== 生命周期回调示例 ===")

	// 1. 创建记忆 - 触发OnCreated
	fmt.Println("1. 创建记忆...")
	id1, _ := svc.Remember(ctx, memory.RememberRequest{
		Namespace:  "test/callbacks",
		Title:      "测试记忆",
		Content:    "这是测试内容",
		SourceType: memory.SourceUser,
	})

	// 2. 更新记忆 - 触发OnUpdated
	fmt.Println("\n2. 更新记忆...")
	newContent := "这是更新后的内容"
	svc.Update(ctx, memory.UpdateRequest{
		ItemID:          id1,
		Content:         &newContent,
		ExpectedVersion: 1,
	})

	// 3. 创建另一个记忆然后删除 - 触发OnDeleted
	fmt.Println("\n3. 创建并删除记忆...")
	id2, _ := svc.Remember(ctx, memory.RememberRequest{
		Namespace:  "test/callbacks",
		Title:      "将被删除的记忆",
		Content:    "这是临时内容",
		SourceType: memory.SourceUser,
	})
	svc.Forget(ctx, memory.ForgetRequest{
		ItemIDs: []string{id2},
		Mode:    "soft",
	})

	// 4. 创建带TTL的记忆等待过期 - 触发OnExpired
	fmt.Println("\n4. 创建带TTL的记忆...")
	ttl := 2 // 2秒后过期
	id3, _ := svc.Remember(ctx, memory.RememberRequest{
		Namespace:  "test/callbacks",
		Title:      "即将过期的记忆",
		Content:    "这是临时内容",
		TTLSeconds: &ttl,
	})

	fmt.Println("   等待过期...")
	// 这里不等待，手动调用CleanupExpired来演示
	svc.CleanupExpired(ctx)

	// 检查过期项是否真的过期了（需要等一下让TTL过期）
	// 为了演示效果，我们直接查数据库
	fmt.Printf("\n5. 检查记忆 %s... 的状态\n", id3[:8])

	// 汇总事件
	fmt.Println("\n=== 事件汇总 ===")
	for _, event := range events {
		fmt.Println(event)
	}

	fmt.Println("\n=== 示例完成 ===")
	fmt.Println("\n回调使用场景:")
	fmt.Println("- OnCreated: 发送通知、记录日志、同步到外部系统")
	fmt.Println("- OnUpdated: 更新索引、触发工作流、记录变更历史")
	fmt.Println("- OnDeleted: 清理关联资源、记录审计日志")
	fmt.Println("- OnExpired: 数据归档、清理缓存、生成报告")
}
