// Example: 重复检测示例
// 展示如何使用dedupe_key防止重复存储
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/lengzhao/memory"
)

func main() {
	ctx := context.Background()
	ctx = memory.WithIsolation(ctx, "demo-tenant", "demo-user", "dedupe-session", "importer")

	// 初始化
	cfg := memory.DefaultConfig()
	cfg.Path = "dedupe_example.db"

	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	svc := memory.NewMemoryService(db)

	fmt.Println("=== 重复检测示例 ===")

	// 场景1: 使用dedupe_key防止重复导入
	fmt.Println("1. 模拟导入外部数据源...")

	// 第一次导入
	externalID := "user_12345_profile"
	id1, err := svc.Remember(ctx, memory.RememberRequest{
		NamespaceType: memory.NamespaceProfile,
		Title:         "用户资料",
		Content:       "用户张三，邮箱 zhangsan@example.com",
		Tags:          []string{"imported", "user"},
		SourceType:    memory.SourceImport,
		DedupeKey:     &externalID, // 使用外部系统ID作为去重键
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   第一次导入: ID = %s\n", id1)

	// 第二次导入（相同externalID）
	id2, err := svc.Remember(ctx, memory.RememberRequest{
		NamespaceType: memory.NamespaceProfile,
		Title:         "用户资料（更新）", // 即使内容不同
		Content:       "用户张三，邮箱 zhangsan@example.com，电话 13800138000",
		Tags:          []string{"imported", "user"},
		SourceType:    memory.SourceImport,
		DedupeKey:     &externalID, // 相同dedupe_key
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   第二次导入: ID = %s\n", id2)

	if id1 == id2 {
		fmt.Println("   ✓ 检测到重复，返回相同ID，未创建新记录")
	} else {
		fmt.Println("   ✗ 创建了重复记录")
	}

	// 场景2: 不同数据源使用不同dedupe_key
	fmt.Println("\n2. 不同用户使用不同dedupe_key...")

	users := []struct {
		id      string
		name    string
		content string
	}{
		{"user_001", "李四", "用户李四，程序员"},
		{"user_002", "王五", "用户王五，设计师"},
		{"user_001", "李四（更新）", "用户李四，高级工程师"}, // 重复ID
	}

	for _, u := range users {
		id, err := svc.Remember(ctx, memory.RememberRequest{
			NamespaceType: memory.NamespaceProfile,
			Title:         u.name,
			Content:       u.content,
			SourceType:    memory.SourceImport,
			DedupeKey:     &u.id,
		})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("   %s -> ID: %s\n", u.name, id)
	}

	// 统计
	hits, _ := svc.Recall(ctx, memory.RecallRequest{
		NamespaceTypes: []memory.NamespaceType{memory.NamespaceProfile},
	})
	fmt.Printf("\n3. 最终导入用户数量: %d (预期3个：张三+李四+王五)\n", len(hits))

	fmt.Println("\n=== 示例完成 ===")
}
