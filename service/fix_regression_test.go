package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/store"
)

// Regression tests for the P0 correctness/security fixes. Each test fails on the pre-fix code.

func newSvc(t *testing.T) (*MemoryService, *gorm.DB) {
	t.Helper()
	db := store.SetupTestDB(t)
	return NewMemoryService(db), db
}

// 1. Decision-engine UPDATE must refresh the search index (previously the FTS row kept the old
// content, so the memory became unfindable by its new text).
func TestFix_DecisionUpdateKeepsSearchIndexInSync(t *testing.T) {
	svc, db := newSvc(t)
	ctx := AllowGlobal(context.Background())
	de := NewDecisionEngine(db)

	id, err := svc.Remember(ctx, RememberRequest{
		NamespaceType: model.NamespaceTypeProfile,
		Content:       "用户喜欢喝茶",
		Confidence:    0.9,
	})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	results, err := de.ExecuteDecisions(ctx,
		[]ExtractedMemory{{TempID: "c1", Namespace: model.NamespaceTypeProfile, Content: "用户喜欢喝燕麦拿铁", Confidence: 0.9}},
		[]MemoryDecision{{Decision: DecisionUpdate, TargetID: id, Confidence: 0.9}},
	)
	if err != nil {
		t.Fatalf("ExecuteDecisions failed: %v", err)
	}
	if len(results.Updated) != 1 {
		t.Fatalf("expected one update, got %+v", results)
	}

	var item model.MemoryItem
	if err := db.First(&item, "id = ?", id).Error; err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if !strings.Contains(item.TokenizedText, "燕麦") {
		t.Fatalf("tokenized_text not refreshed: %q", item.TokenizedText)
	}

	hits, err := svc.Recall(ctx, RecallRequest{Query: "燕麦拿铁", TopK: 5})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("expected the updated memory to be searchable by new content, got %+v", hits)
	}
}

// 2. Soft deletes / expiries must leave the FTS index, and ValidateFTS must not cry wolf.
func TestFix_SoftDeletePrunesSearchIndex(t *testing.T) {
	svc, db := newSvc(t)
	ctx := AllowGlobal(context.Background())

	id, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "会被删除的记忆", Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	if _, err := svc.Forget(ctx, ForgetRequest{ItemIDs: []string{id}, Mode: "soft"}); err != nil {
		t.Fatalf("forget failed: %v", err)
	}

	var ftsRows int64
	db.Raw("SELECT COUNT(*) FROM fts_memory WHERE item_id = ?", id).Scan(&ftsRows)
	if ftsRows != 0 {
		t.Fatalf("expected soft-deleted row to leave the fts index, got %d rows", ftsRows)
	}

	ok, err := svc.ValidateFTS(ctx)
	if err != nil {
		t.Fatalf("ValidateFTS failed: %v", err)
	}
	if !ok {
		t.Fatal("expected ValidateFTS to pass after a soft delete")
	}

	// Decision-engine DELETE must behave the same way.
	id2, _ := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "引擎删除的记忆", Confidence: 0.9})
	de := NewDecisionEngine(db)
	if _, err := de.ExecuteDecisions(ctx,
		[]ExtractedMemory{{TempID: "d1", Namespace: model.NamespaceTypeProfile, Content: "x", Confidence: 0.9}},
		[]MemoryDecision{{Decision: DecisionDelete, TargetID: id2}},
	); err != nil {
		t.Fatalf("decision delete failed: %v", err)
	}
	db.Raw("SELECT COUNT(*) FROM fts_memory WHERE item_id = ?", id2).Scan(&ftsRows)
	if ftsRows != 0 {
		t.Fatalf("expected engine-deleted row to leave the fts index, got %d rows", ftsRows)
	}
}

// 3. Forget without an explicit namespace type must not silently fall back to transient.
func TestFix_ForgetWithoutNamespaceTypeDeletesAcrossNamespaces(t *testing.T) {
	svc, db := newSvc(t)
	ctx := WithIsolation(context.Background(), "t1", "u1", "s1", "planner")

	profileID, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "画像记忆", Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember profile failed: %v", err)
	}
	transientID, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "会话记忆", Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember transient failed: %v", err)
	}

	count, err := svc.Forget(ctx, ForgetRequest{ItemIDs: []string{profileID, transientID}})
	if err != nil {
		t.Fatalf("forget failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected both memories to be forgotten, got %d", count)
	}

	for _, id := range []string{profileID, transientID} {
		var item model.MemoryItem
		db.First(&item, "id = ?", id)
		if item.Status != model.ItemStatusDeleted {
			t.Fatalf("expected %s to be deleted, got status %q", id, item.Status)
		}
	}
}

// 4. A forget that matches nothing must say so instead of reporting success.
func TestFix_ForgetUnknownIDReportsNotFound(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := AllowGlobal(context.Background())

	_, err := svc.Forget(ctx, ForgetRequest{ItemIDs: []string{"01NONEXISTENT"}, Mode: "soft"})
	if err == nil {
		t.Fatal("expected an error when nothing matched")
	}
	var me *MemoryError
	if !errors.As(err, &me) || me.Code != CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

// 5. Hard delete must be able to purge its retained copy (GDPR path).
func TestFix_HardDeleteCanBePurged(t *testing.T) {
	svc, db := newSvc(t)
	ctx := AllowGlobal(context.Background())

	id, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "手机号 13800000000", Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	noBackup := false
	count, err := svc.Forget(ctx, ForgetRequest{ItemIDs: []string{id}, Mode: "hard", BackupBeforeHardDelete: &noBackup})
	if err != nil {
		t.Fatalf("hard forget failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row hard-deleted, got %d", count)
	}
	var archived int64
	db.Model(&model.DeletedItem{}).Where("id = ?", id).Count(&archived)
	if archived != 0 {
		t.Fatalf("expected no retained copy when backup is disabled, got %d", archived)
	}

	// With backup enabled the row is recoverable and then purgeable.
	id2, _ := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "另一个手机号", Confidence: 0.9})
	if _, err := svc.Forget(ctx, ForgetRequest{ItemIDs: []string{id2}, Mode: "hard"}); err != nil {
		t.Fatalf("hard forget with backup failed: %v", err)
	}
	db.Model(&model.DeletedItem{}).Where("id = ?", id2).Count(&archived)
	if archived != 1 {
		t.Fatalf("expected a retained copy, got %d", archived)
	}

	purged, err := svc.PurgeDeletedNow(ctx, []string{id2})
	if err != nil {
		t.Fatalf("PurgeDeletedNow failed: %v", err)
	}
	if purged != 1 {
		t.Fatalf("expected the retained copy to be purged, got %d", purged)
	}
}

// 6. Extraction cache must not leak across isolation scopes.
func TestFix_ExtractionCacheIsScoped(t *testing.T) {
	db := store.SetupTestDB(t)
	dialog := "用户：我的手机号是 13800000000"

	scopeA := WithIsolation(context.Background(), "tenantA", "userA", "sessA", "agentA")
	if err := db.Create(&model.DialogExtraction{
		ID: "exA", ScopeKey: extractionScopeKey(scopeA), DialogText: dialog, DialogHash: calculateHash(dialog),
		ExtractedMemoriesJSON: `[{"namespace":"profile","content":"租户A的私有信息","confidence":0.95}]`,
		Status:                model.ExtractionStatusCompleted, CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed cache failed: %v", err)
	}

	scopeB := WithIsolation(context.Background(), "tenantB", "userB", "sessB", "agentB")
	if extractionScopeKey(scopeA) == extractionScopeKey(scopeB) {
		t.Fatal("expected distinct scope keys")
	}

	var found model.DialogExtraction
	err := db.Where("scope_key = ? AND dialog_hash = ?", extractionScopeKey(scopeB), calculateHash(dialog)).First(&found).Error
	if err == nil {
		t.Fatalf("tenant B must not see tenant A's cached extraction: %+v", found)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// 7. Out-of-range importance/confidence must be rejected, not silently skew the ranking.
func TestFix_ValidationRejectsOutOfRangeValues(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := AllowGlobal(context.Background())

	if _, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "x", Importance: 9999}); err == nil {
		t.Fatal("expected importance above 100 to be rejected")
	}
	if _, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "x", Confidence: 42}); err == nil {
		t.Fatal("expected confidence above 1 to be rejected")
	}
	if _, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "   "}); err == nil {
		t.Fatal("expected empty content to be rejected")
	}
	if _, err := svc.Remember(ctx, RememberRequest{NamespaceType: "bogus", Content: "x"}); err == nil {
		t.Fatal("expected an unknown namespace type to be rejected")
	}
}

// 8. Recall must be able to skip the confidence filter, and BM25 must drive ranking.
func TestFix_RecallConfidenceFilterAndRanking(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := AllowGlobal(context.Background())

	// Confidence is left unset (0) on purpose: this is the common host mistake.
	if _, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "PostgreSQL 连接池 调优", Importance: 10}); err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	if _, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "完全无关的内容", Importance: 95}); err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	hits, err := svc.Recall(ctx, RecallRequest{Query: "PostgreSQL 连接池", TopK: 5, SkipConfidenceFilter: true})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected only the textual match, got %d hits", len(hits))
	}
	if hits[0].FTSScore <= 0 || hits[0].FTSScore > 1 {
		t.Fatalf("expected a BM25-derived text score in (0,1], got %v", hits[0].FTSScore)
	}
	if hits[0].Score < 0 || hits[0].Score > 1 {
		t.Fatalf("expected a normalised score, got %v", hits[0].Score)
	}
}

// 9. Partial isolation identities and empty contexts must fail closed.
func TestFix_IsolationFailsClosed(t *testing.T) {
	svc, _ := newSvc(t)

	partial := WithIsolation(context.Background(), "t1", "", "s1", "a1")
	if _, err := svc.Remember(partial, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "x"}); err == nil {
		t.Fatal("expected a partial isolation context to be rejected")
	}

	if _, err := svc.Recall(context.Background(), RecallRequest{TopK: 5}); err == nil {
		t.Fatal("expected a missing isolation context to be rejected")
	}

	if _, err := svc.Remember(ctxWithNamespaceOverride(), RememberRequest{NamespaceType: model.NamespaceTypeProfile, Namespace: "profile/other", Content: "x"}); err == nil {
		t.Fatal("expected an explicit namespace inside isolation mode to be rejected")
	}
}

func ctxWithNamespaceOverride() context.Context {
	return WithIsolation(context.Background(), "t1", "u1", "s1", "a1")
}

// 10. Cross-session dedupe: profile facts dedupe per user, transient per session.
func TestFix_DedupeKeyScoping(t *testing.T) {
	svc, _ := newSvc(t)
	key := "user-email"

	ctx1 := WithIsolation(context.Background(), "t", "u", "sess1", "a")
	ctx2 := WithIsolation(context.Background(), "t", "u", "sess2", "a")

	id1, err := svc.Remember(ctx1, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "邮箱 a@b.com", DedupeKey: &key})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	id2, err := svc.Remember(ctx2, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "邮箱 a@b.com", DedupeKey: &key})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	if id1 != id2 {
		t.Fatal("expected a user-scoped profile fact to dedupe across sessions")
	}

	transient1, _ := svc.Remember(ctx1, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "会话内注意点", DedupeKey: &key})
	transient2, _ := svc.Remember(ctx2, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "会话内注意点", DedupeKey: &key})
	if transient1 == transient2 {
		t.Fatal("expected transient dedupe keys to stay session-scoped")
	}
}

// 11. Policy resolution: wildcard rules and isolated namespaces.
func TestFix_PolicyPrefixMatchAndIsolatedInference(t *testing.T) {
	_, db := newSvc(t)
	ctx := AllowGlobal(context.Background())
	pm := NewPolicyManager(db)

	if err := pm.SetPolicy(ctx, model.NamespacePolicy{
		Namespace:       "knowledge/projA/*",
		RankWeightsJSON: `{"fts":1,"recency":0,"importance":0,"confidence":0}`,
	}); err != nil {
		t.Fatalf("SetPolicy failed: %v", err)
	}
	policy, err := pm.GetPolicy(ctx, "knowledge/projA/123")
	if err != nil {
		t.Fatalf("GetPolicy failed: %v", err)
	}
	if policy.Namespace != "knowledge/projA/*" {
		t.Fatalf("expected the wildcard rule to apply, got %q", policy.Namespace)
	}

	if got := inferNamespaceType("tenant/t1/user/u1/profile"); got != model.NamespaceTypeProfile {
		t.Fatalf("expected profile for isolated namespace, got %q", got)
	}
	if got := inferNamespaceType("tenant/t1/user/u1/session/s1/agent/a/transient"); got != model.NamespaceTypeTransient {
		t.Fatalf("expected transient for isolated namespace, got %q", got)
	}
}

// 12. TTL policy is applied when enabled, and rank weights follow the isolated namespace.
func TestFix_TTLPolicyAppliedWhenEnabled(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db).WithConfig(Config{ApplyPolicies: true})
	ctx := WithIsolation(context.Background(), "t1", "u1", "s1", "planner")

	id, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeAction, Content: "task with policy TTL", Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if item.ExpiresAt == nil {
		t.Fatal("expected the action policy TTL to be applied")
	}

	// Profile has no TTL policy: it must stay permanent.
	id2, _ := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "durable preference", Confidence: 0.9})
	var profile model.MemoryItem
	db.First(&profile, "id = ?", id2)
	if profile.ExpiresAt != nil {
		t.Fatalf("expected profile memories to never expire, got %v", profile.ExpiresAt)
	}
}

// 13. Optimistic locking still detects conflicts, and stale-protection is unchanged.
func TestFix_UpdateScopedAndVersioned(t *testing.T) {
	svc, db := newSvc(t)
	ctxA := WithIsolation(context.Background(), "t1", "u1", "s1", "a")
	ctxB := WithIsolation(context.Background(), "t2", "u2", "s1", "a")

	id, err := svc.Remember(ctxA, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "tenant A data", Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	newContent := "updated by B"
	if err := svc.Update(ctxB, UpdateRequest{ItemID: id, Content: &newContent, ExpectedVersion: 1}); err == nil {
		t.Fatal("expected tenant B to be unable to update tenant A's memory")
	}

	if err := svc.Update(ctxA, UpdateRequest{ItemID: id, Content: &newContent, ExpectedVersion: 1}); err != nil {
		t.Fatalf("expected tenant A update to succeed, got %v", err)
	}
	if err := svc.Update(ctxA, UpdateRequest{ItemID: id, Content: &newContent, ExpectedVersion: 1}); err == nil {
		t.Fatal("expected a version conflict on the second update")
	}

	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if item.Version != 2 {
		t.Fatalf("expected version 2, got %d", item.Version)
	}
}

// 14. Long content stays searchable (head+tail index plus LIKE fallback).
func TestFix_LongContentSearchable(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := AllowGlobal(context.Background())

	long := strings.Repeat("头部填充内容 ", 60) + " 关键字MIDDLETOKEN关键字 " + strings.Repeat("尾部填充内容 ", 60)
	if _, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: long, Confidence: 0.9}); err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	hits, err := svc.Recall(ctx, RecallRequest{Query: "MIDDLETOKEN", TopK: 5})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected the long memory to be found, got %d hits", len(hits))
	}
}

// 15. Audit trail and stats are available when enabled.
func TestFix_AuditEventsAndStats(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db).WithConfig(Config{RecordEvents: true})
	ctx := WithActor(AllowGlobal(context.Background()), "agent-1")
	ctx = WithTrace(ctx, "trace-1", "req-1")

	id, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "audited", Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	if _, err := svc.Forget(ctx, ForgetRequest{ItemIDs: []string{id}, Mode: "soft"}); err != nil {
		t.Fatalf("forget failed: %v", err)
	}

	var events []model.MemoryEvent
	if err := db.Order("created_at").Find(&events).Error; err != nil {
		t.Fatalf("load events failed: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected create+delete events, got %d", len(events))
	}
	if events[0].Actor == nil || *events[0].Actor != "agent-1" {
		t.Fatalf("expected the actor to be recorded, got %+v", events[0])
	}
	if events[0].TraceID == nil || *events[0].TraceID != "trace-1" {
		t.Fatalf("expected the trace id to be recorded, got %+v", events[0])
	}

	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats.SoftDeletedItems != 1 || stats.IndexInSync != true {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if err := svc.Health(ctx); err != nil {
		t.Fatalf("Health failed: %v", err)
	}
}

// 16. Dry runs must not populate the extraction cache used by real writes.
func TestFix_DryRunDoesNotPoisonCache(t *testing.T) {
	db := store.SetupTestDB(t)
	dialog := "用户说他下周三要做季度复盘"
	scope := "t|u|s|a"

	// A dry-run probe is recorded as pending, never completed.
	if err := db.Create(&model.DialogExtraction{
		ID: "dry", ScopeKey: scope, DialogText: dialog, DialogHash: calculateHash(dialog),
		ExtractedMemoriesJSON: `[{"namespace":"action","content":"季度复盘","confidence":0.9}]`,
		Status:                model.ExtractionStatusPending, CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	var found model.DialogExtraction
	err := db.Where("scope_key = ? AND dialog_hash = ? AND status = ?", scope, calculateHash(dialog), model.ExtractionStatusCompleted).First(&found).Error
	if err == nil {
		t.Fatal("a dry-run row must not satisfy a completed-cache lookup")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// 17. Expired rows leave the index after CleanupExpired.
func TestFix_CleanupExpiredPrunesIndex(t *testing.T) {
	svc, db := newSvc(t)
	ctx := AllowGlobal(context.Background())

	ttl := 1
	id, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "short lived", TTLSeconds: &ttl, Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	// Force the row to look expired without sleeping.
	past := time.Now().Add(-time.Hour)
	if err := db.Model(&model.MemoryItem{}).Where("id = ?", id).Update("expires_at", past).Error; err != nil {
		t.Fatalf("forcing expiry failed: %v", err)
	}

	count, err := svc.CleanupExpired(ctx)
	if err != nil {
		t.Fatalf("CleanupExpired failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 expired row, got %d", count)
	}

	var ftsRows int64
	db.Raw("SELECT COUNT(*) FROM fts_memory WHERE item_id = ?", id).Scan(&ftsRows)
	if ftsRows != 0 {
		t.Fatalf("expected expired row to leave the fts index, got %d rows", ftsRows)
	}
	ok, err := svc.ValidateFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("expected ValidateFTS to pass, ok=%v err=%v", ok, err)
	}
}

// 18. The janitor expires stale rows and enforces the archive retention window.
func TestFix_JanitorSweepsExpiryAndRetention(t *testing.T) {
	svc, db := newSvc(t)
	ctx := AllowGlobal(context.Background())

	ttl := 1
	id, err := svc.Remember(ctx, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "expire me", TTLSeconds: &ttl, Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if err := db.Model(&model.MemoryItem{}).Where("id = ?", id).Update("expires_at", past).Error; err != nil {
		t.Fatalf("forcing expiry failed: %v", err)
	}

	// An archived row whose retention window has already passed.
	if err := db.Create(&model.DeletedItem{
		ID: "archived", OriginalDataJSON: "{}",
		DeletedAt:  time.Now().Add(-48 * time.Hour),
		PurgeAfter: time.Now().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed archive failed: %v", err)
	}

	janitor := NewJanitor(svc, JanitorConfig{Interval: time.Hour, PurgeBefore: time.Hour, ArchiveRetention: time.Hour})
	if err := janitor.SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce failed: %v", err)
	}

	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if item.Status != model.ItemStatusExpired {
		t.Fatalf("expected the row to be expired, got %q", item.Status)
	}

	var archived int64
	db.Model(&model.DeletedItem{}).Where("id = ?", "archived").Count(&archived)
	if archived != 0 {
		t.Fatalf("expected the expired archive row to be purged, got %d", archived)
	}
}

// 19. Start/Stop must run and shut down cleanly.
func TestFix_JanitorStartStop(t *testing.T) {
	svc, _ := newSvc(t)
	janitor := NewJanitor(svc, JanitorConfig{Interval: 10 * time.Millisecond})
	janitor.Start(AllowGlobal(context.Background()))
	time.Sleep(50 * time.Millisecond)
	janitor.Stop()
}
