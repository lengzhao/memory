package test

import (
	"strings"
	"testing"
	"time"

	"github.com/lengzhao/memory"
	"github.com/lengzhao/memory/model"
)

// TestChineseSearch_Basic tests basic Chinese keyword search
func TestChineseSearch_Basic(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create memories with Chinese content
	items := []struct {
		title   string
		content string
	}{
		{
			title:   "北京旅游攻略",
			content: "北京是中国的首都，有很多著名的景点，如故宫、天坛、长城等。",
		},
		{
			title:   "上海美食推荐",
			content: "上海有很多好吃的，小笼包、生煎包是必尝的美食。",
		},
		{
			title:   "清华大学介绍",
			content: "清华大学位于北京海淀区，是中国顶尖的高等学府。",
		},
	}

	for _, item := range items {
		_, err := svc.Remember(ctx, memory.RememberRequest{
			NamespaceType: memory.NamespaceKnowledge,
			Title:         item.title,
			Content:       item.content,
			SourceType:    memory.SourceUser,
			Importance:    80,
			Confidence:    0.95,
		})
		if err != nil {
			t.Fatalf("Failed to remember: %v", err)
		}
	}

	// Search "北京" - should match 2 items (北京旅游、清华大学)
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Query:      "北京",
		TopK:       10,
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}

	// Should find at least 2 items
	if len(hits) < 2 {
		t.Errorf("Expected at least 2 hits for '北京', got %d", len(hits))
	}

	// Verify the expected items are found
	foundBeijing := false
	foundTsinghua := false
	for _, hit := range hits {
		if strings.Contains(hit.Title, "北京旅游") {
			foundBeijing = true
		}
		if strings.Contains(hit.Title, "清华大学") {
			foundTsinghua = true
		}
	}
	if !foundBeijing {
		t.Error("Expected to find '北京旅游攻略' in results")
	}
	if !foundTsinghua {
		t.Error("Expected to find '清华大学介绍' in results")
	}

	t.Logf("Found %d hits for '北京': %v", len(hits), getHitTitles(hits))
}

// TestChineseSearch_TopKRanking tests that TopK properly limits and ranks results
func TestChineseSearch_TopKRanking(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create 10 memories with varying importance
	contents := []struct {
		title      string
		content    string
		importance int
	}{
		{"Python教程", "Python是流行的编程语言，简单易学，适合初学者", 95},
		{"Go语言入门", "Go语言适合并发编程，性能优秀，由Google开发", 90},
		{"Java学习笔记", "Java是企业级开发常用语言，生态丰富", 85},
		{"Rust编程", "Rust注重安全性和性能，适合系统级开发", 80},
		{"JavaScript基础", "JavaScript是前端必备语言，浏览器原生支持", 75},
		{"C++进阶", "C++适合系统级开发，性能极高", 70},
		{"Ruby开发", "Ruby语法优雅，适合快速开发Web应用", 65},
		{"Swift入门", "Swift用于iOS开发，由Apple推出", 60},
		{"Kotlin教程", "Kotlin是Android开发首选，与Java互操作", 55},
		{"TypeScript实战", "TypeScript是带类型的JavaScript，适合大型项目", 50},
	}

	for _, item := range contents {
		_, err := svc.Remember(ctx, memory.RememberRequest{
			NamespaceType: memory.NamespaceKnowledge,
			Title:         item.title,
			Content:       item.content,
			SourceType:    memory.SourceUser,
			Importance:    item.importance,
			Confidence:    0.95,
		})
		if err != nil {
			t.Fatalf("Failed to remember: %v", err)
		}
	}

	// Search "语言" which should match multiple items
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Query:      "语言",
		TopK:       5, // Limit to 5 results
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}

	// Verify TopK is respected
	if len(hits) > 5 {
		t.Errorf("Expected at most 5 hits with TopK=5, got %d", len(hits))
	}

	// Verify results are ranked by importance (higher first)
	if len(hits) >= 2 {
		for i := 0; i < len(hits)-1; i++ {
			if hits[i].Importance < hits[i+1].Importance {
				t.Errorf("Results not properly ranked: hit[%d].Importance=%d < hit[%d].Importance=%d",
					i, hits[i].Importance, i+1, hits[i+1].Importance)
			}
		}
	}

	t.Logf("Found %d hits for '语言' (TopK=5), highest importance: %d, lowest: %d",
		len(hits), hits[0].Importance, hits[len(hits)-1].Importance)
}

// TestChineseSearch_UpdateReindex tests that updating content reindexes FTS
func TestChineseSearch_UpdateReindex(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create item about Python
	id, err := svc.Remember(ctx, memory.RememberRequest{
		NamespaceType: memory.NamespaceKnowledge,
		Title:         "Python教程",
		Content:       "Python是流行的编程语言，适合数据分析",
		SourceType:    memory.SourceUser,
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Verify searchable by "Python"
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Query:      "Python",
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("Expected 1 hit for 'Python', got %d", len(hits))
	}

	// Get current version
	var item model.MemoryItem
	if err := tdb.DB.WithContext(ctx).First(&item, "id = ?", id).Error; err != nil {
		t.Fatalf("Failed to get item: %v", err)
	}

	// Update to Go-related content
	newTitle := "Go语言教程"
	newContent := "Go语言适合并发编程，由Google开发，性能优秀"
	err = svc.Update(ctx, memory.UpdateRequest{
		ItemID:          id,
		Title:           &newTitle,
		Content:         &newContent,
		ExpectedVersion: item.Version,
	})
	if err != nil {
		t.Fatalf("Failed to update: %v", err)
	}

	// Small delay for async operations
	time.Sleep(50 * time.Millisecond)

	// Search "Go" should now find the updated item
	hits, err = svc.Recall(ctx, memory.RecallRequest{
		Query:      "Go",
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("Expected 1 hit for 'Go' after update, got %d", len(hits))
	}
	if hits[0].Title != "Go语言教程" {
		t.Errorf("Expected updated title 'Go语言教程', got %q", hits[0].Title)
	}

	t.Logf("After update, search 'Go' found: %s", hits[0].Title)
}

// TestChineseSearch_MixedCJKEnglish tests searching with mixed Chinese and English
func TestChineseSearch_MixedCJKEnglish(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create items with mixed CJK and English
	items := []struct {
		title   string
		content string
	}{
		{
			title:   "iPhone使用技巧",
			content: "iPhone是Apple公司的智能手机，iOS系统流畅好用，适合日常使用。",
		},
		{
			title:   "Android开发指南",
			content: "Android是Google的移动操作系统，基于Linux内核，开源免费。",
		},
		{
			title:   "Git工作流",
			content: "Git是分布式版本控制系统，由Linus Torvalds创建，适合团队协作。",
		},
	}

	for _, item := range items {
		_, err := svc.Remember(ctx, memory.RememberRequest{
			NamespaceType: memory.NamespaceKnowledge,
			Title:         item.title,
			Content:       item.content,
			SourceType:    memory.SourceUser,
		})
		if err != nil {
			t.Fatalf("Failed to remember: %v", err)
		}
	}

	// Test searching English terms
	testCases := []struct {
		query       string
		minHits     int
		maxHits     int
		description string
	}{
		{"iPhone", 1, 1, "English brand name"},
		{"Apple", 1, 1, "English company name"},
		{"Android", 1, 1, "English term"},
		{"Google", 1, 1, "English company name"},
		{"Git", 1, 1, "English term"},
		{"智能手机", 1, 1, "Chinese term from content"},
		// Tokenized full-text search may return additional semantically related items.
		{"操作系统", 1, 10, "Chinese term from content"},
	}

	for _, tc := range testCases {
		hits, err := svc.Recall(ctx, memory.RecallRequest{
			Query:      tc.query,
			TopK:       10,
		})
		if err != nil {
			t.Errorf("Failed to recall for '%s': %v", tc.query, err)
			continue
		}
		if len(hits) < tc.minHits || len(hits) > tc.maxHits {
			t.Errorf("%s: expected %d-%d hit(s) for '%s', got %d", tc.description, tc.minHits, tc.maxHits, tc.query, len(hits))
		} else {
			t.Logf("%s: search '%s' found %d hit(s): %v", tc.description, tc.query, len(hits), getHitTitles(hits))
		}
	}
}

// TestChineseSearch_MultiWordMatch tests matching multiple Chinese words
func TestChineseSearch_MultiWordMatch(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create memories with overlapping keywords
	items := []struct {
		title   string
		content string
		importance int
	}{
		{
			title:   "北京上海高铁",
			content: "北京到上海的高铁很快，只需要4个多小时。",
			importance: 90,
		},
		{
			title:   "北京广州航班",
			content: "北京到广州的航班每天都有很多班次。",
			importance: 80,
		},
		{
			title:   "上海深圳自驾",
			content: "上海到深圳自驾游需要经过浙江福建。",
			importance: 70,
		},
	}

	for _, item := range items {
		_, err := svc.Remember(ctx, memory.RememberRequest{
			NamespaceType: memory.NamespaceKnowledge,
			Title:         item.title,
			Content:       item.content,
			SourceType:    memory.SourceUser,
			Importance:    item.importance,
		})
		if err != nil {
			t.Fatalf("Failed to remember: %v", err)
		}
	}

	// Search "北京" should return 2 items (北京上海、北京广州)
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Query:      "北京",
		TopK:       10,
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}

	if len(hits) != 2 {
		t.Errorf("Expected 2 hits for '北京', got %d: %v", len(hits), getHitTitles(hits))
	}

	// Verify ranking (higher importance first)
	if len(hits) == 2 && hits[0].Importance < hits[1].Importance {
		t.Error("Results not ranked by importance (higher first)")
	}

	t.Logf("Search '北京' found %d hits, ranked by importance", len(hits))

	// Search "上海" should return 2 items (北京上海、上海深圳)
	hits, err = svc.Recall(ctx, memory.RecallRequest{
		Query:      "上海",
		TopK:       10,
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}

	if len(hits) != 2 {
		t.Errorf("Expected 2 hits for '上海', got %d: %v", len(hits), getHitTitles(hits))
	}

	t.Logf("Search '上海' found %d hits: %v", len(hits), getHitTitles(hits))
}

// getHitTitles extracts titles from hits for logging
func getHitTitles(hits []memory.MemoryHit) []string {
	titles := make([]string, len(hits))
	for i, hit := range hits {
		titles[i] = hit.Title
	}
	return titles
}

// TestChineseSearch_LongContentTruncation tests that long content is properly truncated for indexing
func TestChineseSearch_LongContentTruncation(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create a memory with very long content (simulating quoted content)
	// The key info is at the beginning and end, middle is noise
	longContent := "这是一篇关于北京旅游的攻略，开头提到了天安门和故宫。" +
		strings.Repeat("中间有大量的引用内容和其他信息，可能包含一些不重要的文字。", 20) +
		"结尾提到了烤鸭和美食推荐，这是最重要的部分。"

	_, err := svc.Remember(ctx, memory.RememberRequest{
		NamespaceType: memory.NamespaceKnowledge,
		Title:         "北京攻略",
		Content:       longContent,
		SourceType:    memory.SourceUser,
		Importance:    90,
	})
	if err != nil {
		t.Fatalf("Failed to remember: %v", err)
	}

	// Search for keywords that appear at the END of long content
	// Should still be found because we keep tail 100 chars
	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Query:      "烤鸭美食",
		TopK:       2,
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}

	if len(hits) != 1 {
		t.Errorf("Expected 1 hit for '烤鸭美食' (at end of long content), got %d", len(hits))
	}

	t.Logf("Search '烤鸭美食' in long content found %d hit(s)", len(hits))

	// Also verify beginning keywords are indexed
	hits, err = svc.Recall(ctx, memory.RecallRequest{
		Query:      "天安门故宫",
		TopK:       2,
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}

	if len(hits) != 1 {
		t.Errorf("Expected 1 hit for '天安门故宫' (at start of long content), got %d", len(hits))
	}

	t.Logf("Search '天安门故宫' in long content found %d hit(s)", len(hits))
}

// TestChineseSearch_SentenceQuery tests searching with a full sentence
// Matches partial keywords and ranks by relevance, returns top 2 results
func TestChineseSearch_SentenceQuery(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.Cleanup()

	svc := memory.NewMemoryService(tdb.DB)
	ctx := testContext()

	// Create diverse memories about travel and cities
	items := []struct {
		title      string
		content    string
		importance int
	}{
		{
			title:      "北京旅游攻略",
			content:    "北京是中国的首都，有很多著名的景点，如故宫、天坛、长城等。美食有北京烤鸭、炸酱面。",
			importance: 95,
		},
		{
			title:      "上海美食推荐",
			content:    "上海有很多好吃的，小笼包、生煎包是必尝的美食。南京路步行街也很热闹。",
			importance: 85,
		},
		{
			title:      "广州购物指南",
			content:    "广州是购物天堂，天河城、上下九步行街有很多商场。美食也很多，早茶很有名。",
			importance: 80,
		},
		{
			title:      "深圳科技产业",
			content:    "深圳是中国的科技中心，有华为、腾讯等知名企业。创新创业氛围很好。",
			importance: 75,
		},
		{
			title:      "杭州西湖游记",
			content:    "西湖是杭州的标志性景点，断桥残雪、雷峰塔都很有名。适合周末游玩。",
			importance: 70,
		},
	}

	for _, item := range items {
		_, err := svc.Remember(ctx, memory.RememberRequest{
			NamespaceType: memory.NamespaceKnowledge,
			Title:         item.title,
			Content:       item.content,
			SourceType:    memory.SourceUser,
			Importance:    item.importance,
		})
		if err != nil {
			t.Fatalf("Failed to remember: %v", err)
		}
	}

	// Test: Search with a sentence "我想去北京旅游吃美食"
	// Should match records containing ANY of these keywords: 北京, 旅游, 美食
	sentence := "我想去北京旅游吃美食"
	t.Logf("Searching with sentence: '%s'", sentence)

	hits, err := svc.Recall(ctx, memory.RecallRequest{
		Query:      sentence,
		TopK:       2, // Only return top 2
	})
	if err != nil {
		t.Fatalf("Failed to recall: %v", err)
	}

	// Should return exactly 2 results
	if len(hits) != 2 {
		t.Errorf("Expected exactly 2 hits with TopK=2, got %d", len(hits))
	}

	t.Logf("Top 2 results for sentence query '%s':", sentence)
	for i, hit := range hits {
		t.Logf("  %d. [%s] %s (importance: %d, score: %.3f)",
			i+1, hit.NamespaceType, hit.Title, hit.Importance, hit.Score)
	}

	// Verify the results make sense:
	// "北京旅游攻略" should be #1 (matches 北京+旅游, highest importance 95)
	// "上海美食推荐" should be #2 (matches 美食, importance 85)

	if len(hits) >= 1 {
		if !strings.Contains(hits[0].Title, "北京") && !strings.Contains(hits[0].Content, "北京") {
			t.Errorf("Expected #1 result to contain '北京', got: %s", hits[0].Title)
		}
	}

	// Verify ranking is by importance (descending)
	if len(hits) == 2 {
		if hits[0].Importance < hits[1].Importance {
			t.Error("Results not properly ranked by importance (higher should be first)")
		}
	}
}
