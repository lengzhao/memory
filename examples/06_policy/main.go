// Example: Namespace策略管理示例
// 展示如何使用PolicyManager管理不同namespace的策略
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
	cfg.Path = "policy_example.db"

	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	memory.Migrate(db)

	fmt.Println("=== Namespace策略管理示例 ===")

	// 1. 创建PolicyManager
	pm := memory.NewPolicyManager(db)

	// 2. 获取默认策略（不同类型namespace的默认行为）
	fmt.Println("1. 查看各namespace类型的默认策略：")

	nsTypes := []model.NamespaceType{
		memory.NamespaceTransient,
		memory.NamespaceProfile,
		memory.NamespaceAction,
		memory.NamespaceKnowledge,
	}

	for _, nsType := range nsTypes {
		// 使用通配符namespace获取该类型的默认策略
		ns := string(nsType) + "/*"
		policy, err := pm.GetPolicy(ctx, ns)
		if err != nil {
			log.Fatal(err)
		}

		ttl := "永不过期"
		if policy.TTLSeconds != nil {
			ttl = fmt.Sprintf("%d秒 (%v)", *policy.TTLSeconds, policy.TTLPolicy)
		}

		fts, recency, importance, confidence := pm.GetRankWeights(policy)

		fmt.Printf("   [%s]\n", nsType)
		fmt.Printf("     TTL: %s\n", ttl)
		fmt.Printf("     排序权重: FTS=%.2f, 新鲜度=%.2f, 重要性=%.2f, 置信度=%.2f\n",
			fts, recency, importance, confidence)
		fmt.Printf("     默认TopK: %d\n\n", policy.DefaultTopK)
	}

	// 3. 为特定namespace设置自定义策略
	fmt.Println("2. 为特定项目设置自定义策略：")

	// 为项目A的任务设置更短的TTL（任务需要更频繁清理）
	projectATTL := 7 * 24 * 3600 // 7天
	projectAPolicy := model.NamespacePolicy{
		Namespace:                 "action/project-a",
		TTLSeconds:                &projectATTL,
		TTLPolicy:                 model.TTLPolicyFixed,
		SummaryEnabled:            true,
		SummaryItemTokenThreshold: 300,
		RankWeightsJSON:           `{"fts":0.40,"recency":0.35,"importance":0.15,"confidence":0.10}`,
		DefaultTopK:               20,
	}

	if err := pm.SetPolicy(ctx, projectAPolicy); err != nil {
		log.Fatal(err)
	}
	fmt.Println("   ✓ 为 action/project-a 设置了7天TTL策略")

	// 为高优先级知识库设置永不过期
	kbVIPPolicy := model.NamespacePolicy{
		Namespace:                 "knowledge/vip",
		TTLSeconds:                nil, // 永不过期
		TTLPolicy:                 model.TTLPolicyManual,
		SummaryEnabled:            true,
		SummaryItemTokenThreshold: 2000,
		RankWeightsJSON:           `{"fts":0.70,"recency":0.05,"importance":0.15,"confidence":0.10}`,
		DefaultTopK:               5,
	}

	if err := pm.SetPolicy(ctx, kbVIPPolicy); err != nil {
		log.Fatal(err)
	}
	fmt.Println("   ✓ 为 knowledge/vip 设置了永不过期策略（强调FTS搜索）")

	// 4. 使用自定义策略创建记忆
	fmt.Println("\n3. 使用自定义策略创建记忆：")
	svc := memory.NewMemoryService(db)

	// 为project-a创建任务（会应用我们设置的7天TTL）
	taskID, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "action/project-a",
		NamespaceType: memory.NamespaceAction,
		Title:         "项目A的紧急任务",
		Content:       "需要在一周内完成API接口开发",
		Importance:    90,
		Confidence:    1.0,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   创建任务: %s (应用project-a的7天TTL策略)\n", taskID)

	// 为vip知识库创建知识（永不过期）
	kbID, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "knowledge/vip",
		NamespaceType: memory.NamespaceKnowledge,
		Title:         "VIP客户信息",
		Content:       "重要客户的关键业务信息",
		Importance:    100,
		Confidence:    1.0,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   创建知识: %s (应用vip的永不过期策略)\n", kbID)

	// 为普通knowledge创建知识（使用默认策略 - 永不过期但不同权重）
	normalKbID, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "knowledge/general",
		NamespaceType: memory.NamespaceKnowledge,
		Title:         "一般知识点",
		Content:       "普通的知识条目",
		Importance:    50,
		Confidence:    0.9,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   创建知识: %s (使用knowledge默认策略)\n", normalKbID)

	// 5. 验证策略应用
	fmt.Println("\n4. 验证策略已正确应用：")

	// 检查project-a任务是否有TTL
	var taskItem model.MemoryItem
	db.WithContext(ctx).First(&taskItem, "id = ?", taskID)
	if taskItem.ExpiresAt != nil {
		fmt.Printf("   ✓ 任务有过期时间: %v\n", taskItem.ExpiresAt.Format("2006-01-02 15:04:05"))
	}

	// 检查vip知识是否无TTL
	var vipItem model.MemoryItem
	db.WithContext(ctx).First(&vipItem, "id = ?", kbID)
	if vipItem.ExpiresAt == nil {
		fmt.Println("   ✓ VIP知识无过期时间（永不过期）")
	}

	// 6. 获取精确匹配的策略
	fmt.Println("\n5. 获取精确匹配的策略：")

	customPolicy, _ := pm.GetPolicy(ctx, "action/project-a")
	fmt.Printf("   action/project-a: TTL=%d秒\n", *customPolicy.TTLSeconds)

	defaultPolicy, _ := pm.GetPolicy(ctx, "action/other-project")
	fmt.Printf("   action/other-project: 使用默认策略 TTL=%d秒\n", *defaultPolicy.TTLSeconds)

	fmt.Println("\n=== 示例完成 ===")
	fmt.Println("\nPolicyManager使用场景：")
	fmt.Println("- 为不同项目/用户设置不同的TTL策略")
	fmt.Println("- 自定义搜索排序权重（FTS vs 新鲜度 vs 重要性）")
	fmt.Println("- 控制是否启用自动摘要")
	fmt.Println("- 设置默认召回数量(TopK)")
}
