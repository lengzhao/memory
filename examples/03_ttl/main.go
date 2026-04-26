// Example: TTL（生存时间）示例
// 展示如何使用TTL自动过期记忆
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/lengzhao/memory"
)

func main() {
	ctx := context.Background()

	// 初始化
	cfg := memory.DefaultConfig()
	cfg.Path = "ttl_example.db"

	db, err := memory.InitDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close(db)

	svc := memory.NewMemoryService(db)

	fmt.Println("=== TTL（生存时间）示例 ===")

	// 场景1: 会话上下文（短TTL）
	fmt.Println("1. 创建会话上下文（5秒TTL）...")
	sessionTTL := 5
	sessionID, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "session/active",
		NamespaceType: memory.NamespaceTransient,
		Title:         "当前会话",
		Content:       "用户正在浏览产品页面",
		SourceType:    memory.SourceSystem,
		TTLSeconds:    &sessionTTL,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   会话创建: ID = %s\n", sessionID)

	// 场景2: 临时验证码（中等TTL）
	fmt.Println("\n2. 创建验证码（60秒TTL）...")
	codeTTL := 60
	codeID, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "system/verification",
		NamespaceType: memory.NamespaceTransient,
		Title:         "验证码",
		Content:       "验证码: 123456，用于登录验证",
		Tags:          []string{"verification", "auth"},
		SourceType:    memory.SourceSystem,
		TTLSeconds:    &codeTTL,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   验证码创建: ID = %s (60秒后过期)\n", codeID)

	// 场景3: 用户偏好（长TTL）
	fmt.Println("\n3. 创建用户偏好（1小时TTL）...")
	prefTTL := 3600 // 1小时
	prefID, err := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "user/temp-preferences",
		NamespaceType: memory.NamespaceProfile,
		Title:         "临时主题设置",
		Content:       "用户选择了暗黑模式（实验性功能）",
		Tags:          []string{"preference", "ui", "theme"},
		SourceType:    memory.SourceUser,
		TTLSeconds:    &prefTTL,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   偏好创建: ID = %s (1小时后过期)\n", prefID)

	// 检查当前状态
	fmt.Println("\n4. 当前活跃记忆...")
	hits, _ := svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"session/active", "system/verification", "user/temp-preferences"},
	})
	fmt.Printf("   活跃记忆数量: %d\n", len(hits))
	for _, hit := range hits {
		expiresIn := "永不过期"
		if hit.ExpiresAt != nil {
			duration := time.Until(*hit.ExpiresAt)
			expiresIn = fmt.Sprintf("%d秒后过期", int(duration.Seconds()))
		}
		fmt.Printf("   - %s: %s (%s)\n", hit.Title, hit.Content, expiresIn)
	}

	// 等待会话过期
	fmt.Println("\n5. 等待5秒让会话过期...")
	time.Sleep(6 * time.Second)

	// 清理过期项
	fmt.Println("6. 执行过期清理...")
	count, err := svc.CleanupExpired(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("   清理了 %d 条过期记录\n", count)

	// 检查过期后状态
	fmt.Println("\n7. 过期后的活跃记忆...")
	hits, _ = svc.Recall(ctx, memory.RecallRequest{
		Namespaces: []string{"session/active", "system/verification", "user/temp-preferences"},
	})
	fmt.Printf("   活跃记忆数量: %d (会话应该已过期)\n", len(hits))
	for _, hit := range hits {
		expiresIn := "永不过期"
		if hit.ExpiresAt != nil {
			duration := time.Until(*hit.ExpiresAt)
			if duration > 0 {
				expiresIn = fmt.Sprintf("%d秒后过期", int(duration.Seconds()))
			} else {
				expiresIn = "已过期"
			}
		}
		fmt.Printf("   - %s (%s)\n", hit.Title, expiresIn)
	}

	// 场景4: 手动续期
	fmt.Println("\n8. 手动续期验证码...")
	newTTL := 300 // 延长到5分钟
	if err := svc.RenewExpiration(ctx, codeID, newTTL); err != nil {
		log.Fatal(err)
	}
	fmt.Println("   验证码已续期到5分钟后过期")

	// 场景5: 滑动TTL（基于访问次数续期）
	fmt.Println("\n9. 模拟滑动TTL（每3次访问续期）...")
	slideTTL := 10
	slideID, _ := svc.Remember(ctx, memory.RememberRequest{
		Namespace:     "cache/data",
		NamespaceType: memory.NamespaceTransient,
		Title:         "缓存数据",
		Content:       "频繁访问的查询结果",
		TTLSeconds:    &slideTTL,
	})

	for i := 1; i <= 5; i++ {
		renewed, _ := svc.TouchWithRenew(ctx, slideID, 3, 30) // 每3次访问续期30秒
		if renewed {
			fmt.Printf("   第%d次访问: TTL已续期\n", i)
		} else {
			fmt.Printf("   第%d次访问: 未达续期阈值\n", i)
		}
	}

	fmt.Println("\n=== 示例完成 ===")
}
