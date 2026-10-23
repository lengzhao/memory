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

func newSvc(t *testing.T) (*MemoryService, *gorm.DB) {
	t.Helper()
	db := store.SetupTestDB(t)
	return NewMemoryService(db), db
}

func testScope() Scope {
	return MustScope("t1", "u1", "s1", "agent")
}

func otherSessionScope() Scope {
	return MustScope("t1", "u1", "s2", "agent")
}

func otherTenantScope() Scope {
	return MustScope("t2", "u2", "s1", "agent")
}

// ---- Scope ---------------------------------------------------------------------------------

func TestScope_ValidationAndLayout(t *testing.T) {
	if _, err := NewScope("", "u1", "s1", "a1"); err == nil {
		t.Fatal("expected an incomplete scope to be rejected")
	}
	if _, err := NewScope("t", "u", "s", ""); err == nil {
		t.Fatal("expected a missing agent to be rejected")
	}

	scope := testScope()
	want := map[model.NamespaceType]string{
		model.NamespaceTypeProfile:   "tenant/t1/user/u1/profile",
		model.NamespaceTypeAction:    "tenant/t1/user/u1/action",
		model.NamespaceTypeKnowledge: "tenant/t1/user/u1/knowledge",
		model.NamespaceTypeTransient: "tenant/t1/user/u1/session/s1/agent/agent/transient",
	}
	for nsType, expected := range want {
		if got := scope.Namespace(nsType); got != expected {
			t.Errorf("namespace %s: got %q want %q", nsType, got, expected)
		}
	}

	// Session isolation applies to transient rows only: profile/action/knowledge are user-scoped,
	// so two sessions of the same user share them while their transient namespaces differ.
	if otherSessionScope().Namespace(model.NamespaceTypeProfile) != scope.Namespace(model.NamespaceTypeProfile) {
		t.Fatal("expected sessions of the same user to share the profile namespace")
	}
	if otherSessionScope().Namespace(model.NamespaceTypeTransient) == scope.Namespace(model.NamespaceTypeTransient) {
		t.Fatal("expected transient namespaces to be session-specific")
	}
	if otherTenantScope().Namespace(model.NamespaceTypeProfile) == scope.Namespace(model.NamespaceTypeProfile) {
		t.Fatal("expected tenants to have disjoint namespaces")
	}

	if !scope.Owns(scope.Namespace(model.NamespaceTypeProfile)) {
		t.Fatal("expected a scope to own its own namespace")
	}
	if scope.Owns(otherTenantScope().Namespace(model.NamespaceTypeProfile)) {
		t.Fatal("expected a scope not to own another tenant's namespace")
	}

	global := GlobalScope()
	if !global.IsGlobal() || global.Namespace(model.NamespaceTypeProfile) != "profile/default" {
		t.Fatalf("unexpected global scope: %+v", global)
	}

	recovered, ok := ScopeFromNamespace(scope.Namespace(model.NamespaceTypeTransient))
	if !ok || recovered != scope {
		t.Fatalf("ScopeFromNamespace roundtrip failed: %+v", recovered)
	}
}

func TestScope_NamespaceTypeInference(t *testing.T) {
	cases := map[string]model.NamespaceType{
		"profile/default":                                model.NamespaceTypeProfile,
		"knowledge/topic":                                model.NamespaceTypeKnowledge,
		"tenant/t1/user/u1/profile":                      model.NamespaceTypeProfile,
		"tenant/t1/user/u1/knowledge":                    model.NamespaceTypeKnowledge,
		"tenant/t1/user/u1/session/s1/agent/a/transient": model.NamespaceTypeTransient,
		"tenant/t1/user/u1/session/s1/agent/a/action":    model.NamespaceTypeAction,
	}
	for namespace, expected := range cases {
		if got := inferScopeNamespaceType(namespace); got != expected {
			t.Errorf("inferScopeNamespaceType(%q) = %q, want %q", namespace, got, expected)
		}
	}
}

// ---- Remember / validation ------------------------------------------------------------------

func TestRemember_StoresAndValidates(t *testing.T) {
	svc, db := newSvc(t)
	scope := testScope()

	id, err := svc.Remember(context.Background(), scope, RememberRequest{
		NamespaceType: model.NamespaceTypeProfile,
		Content:       "prefers dark theme",
		Importance:    80,
		Confidence:    0.95,
	})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	var item model.MemoryItem
	if err := db.First(&item, "id = ?", id).Error; err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if item.Namespace != scope.Namespace(model.NamespaceTypeProfile) {
		t.Fatalf("unexpected namespace %q", item.Namespace)
	}
	if item.Status != model.ItemStatusActive || item.Version != 1 {
		t.Fatalf("unexpected row state: %+v", item)
	}
	if item.TokenizedText == "" {
		t.Fatal("expected the tokenized text to be populated")
	}

	invalid := []RememberRequest{
		{NamespaceType: model.NamespaceTypeProfile, Content: "   "},
		{NamespaceType: model.NamespaceTypeProfile, Content: "x", Importance: 101},
		{NamespaceType: model.NamespaceTypeProfile, Content: "x", Confidence: 1.5},
		{NamespaceType: "bogus", Content: "x"},
	}
	for i, req := range invalid {
		if _, err := svc.Remember(context.Background(), scope, req); err == nil {
			t.Fatalf("case %d: expected a validation error", i)
		}
	}
}

func TestRemember_DedupeKeyScoping(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	key := "user-email"

	// A profile fact is shared across the user's sessions, so the key dedupes across them.
	session1, session2 := testScope(), otherSessionScope()
	first, err := svc.Remember(ctx, session1, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "a@b.com", DedupeKey: &key, Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	second, err := svc.Remember(ctx, session2, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "a@b.com", DedupeKey: &key, Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	if first != second {
		t.Fatal("expected a user-scoped key to dedupe across sessions")
	}

	// Transient rows are session-scoped, so the same key legitimately produces two memories.
	t1, _ := svc.Remember(ctx, session1, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "note", DedupeKey: &key, Confidence: 0.9})
	t2, _ := svc.Remember(ctx, session2, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "note", DedupeKey: &key, Confidence: 0.9})
	if t1 == t2 {
		t.Fatal("expected transient keys to stay session-scoped")
	}

	// Different tenants never collide, even with an identical key.
	other, _ := svc.Remember(ctx, otherTenantScope(), RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "a@b.com", DedupeKey: &key, Confidence: 0.9})
	if other == first {
		t.Fatal("expected tenants to have independent dedupe domains")
	}
}

func TestRemember_ConcurrentDedupeIsIdempotent(t *testing.T) {
	svc, db := newSvc(t)
	scope := testScope()
	key := "concurrent-key"
	req := RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "same content", DedupeKey: &key, Confidence: 0.9}

	const workers = 12
	ids := make([]string, workers)
	errs := make([]error, workers)
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			ids[idx], errs[idx] = svc.Remember(context.Background(), scope, req)
		}(i)
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d failed: %v", i, err)
		}
	}

	var count int64
	db.Model(&model.MemoryItem{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected exactly one row from %d concurrent idempotent writes, got %d", workers, count)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("expected all writers to observe the same id, got %v", ids)
		}
	}
}

func TestRemember_TTLAndPolicy(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db).WithConfig(Config{ApplyPolicies: true})
	scope := testScope()
	ctx := context.Background()

	// Explicit TTL always wins.
	ttl := 60
	id, err := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "explicit ttl", TTLSeconds: &ttl, Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if item.ExpiresAt == nil {
		t.Fatal("expected an explicit TTL to be applied")
	}

	// Policy TTL applies to action/transient, not to profile/knowledge.
	actionID, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeAction, Content: "task", Confidence: 0.9})
	profileID, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "durable", Confidence: 0.9})

	var action model.MemoryItem
	if err := db.First(&action, "id = ?", actionID).Error; err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if action.ExpiresAt == nil {
		t.Fatal("expected the action namespace policy TTL to be applied")
	}

	var profile model.MemoryItem
	if err := db.First(&profile, "id = ?", profileID).Error; err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if profile.ExpiresAt != nil {
		t.Fatalf("expected profile memories to never expire, got %v", profile.ExpiresAt)
	}
}

// ---- Recall ---------------------------------------------------------------------------------

func TestRecall_RanksByRelevanceAndScope(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "PostgreSQL 连接池 调优 参数", Importance: 10, Confidence: 0.9})
	svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "完全无关的内容", Importance: 95, Confidence: 0.9})
	// Same user, another session: the knowledge namespace is user-scoped, so this IS visible.
	svc.Remember(ctx, otherSessionScope(), RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "PostgreSQL 高可用方案", Confidence: 0.9})
	// Another tenant must never leak into the results.
	svc.Remember(ctx, otherTenantScope(), RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "PostgreSQL 机密 配置", Confidence: 0.9})

	hits, err := svc.Recall(ctx, scope, RecallRequest{Query: "PostgreSQL 连接池", TopK: 10})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected the two tenant-visible matches, got %d: %+v", len(hits), hits)
	}
	for _, hit := range hits {
		if strings.Contains(hit.Content, "机密") {
			t.Fatal("another tenant's memory leaked into the recall")
		}
		if hit.FTSScore <= 0 || hit.FTSScore > 1 {
			t.Fatalf("expected a BM25-derived score in (0,1], got %v", hit.FTSScore)
		}
		if hit.Score < 0 || hit.Score > 1 {
			t.Fatalf("expected a normalised score, got %v", hit.Score)
		}
	}
	// The item matching both query tokens must outrank the one matching a single token.
	if !strings.Contains(hits[0].Content, "调优") {
		t.Fatalf("expected the closer match first, got %q", hits[0].Content)
	}
}

func TestRecall_TransientIsSessionScoped(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	session1, session2 := testScope(), otherSessionScope()
	transient := []model.NamespaceType{model.NamespaceTypeTransient}

	svc.Remember(ctx, session1, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "from-session-1", Confidence: 0.9})
	svc.Remember(ctx, session2, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "from-session-2", Confidence: 0.9})

	hits1, _ := svc.Recall(ctx, session1, RecallRequest{NamespaceTypes: transient, TopK: 10})
	hits2, _ := svc.Recall(ctx, session2, RecallRequest{NamespaceTypes: transient, TopK: 10})
	if len(hits1) != 1 || hits1[0].Content != "from-session-1" {
		t.Fatalf("session 1 saw %+v", hits1)
	}
	if len(hits2) != 1 || hits2[0].Content != "from-session-2" {
		t.Fatalf("session 2 saw %+v", hits2)
	}
}

func TestRecall_ConfidenceFilter(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()
	knowledge := []model.NamespaceType{model.NamespaceTypeKnowledge}

	// A host that does not score confidence omits the field: the column default (1.0) applies, and
	// the memory stays recallable at the default floor.
	if _, err := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "unscored memory"}); err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	var unscored model.MemoryItem
	if err := db.First(&unscored, "content = ?", "unscored memory").Error; err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if unscored.Confidence != 1.0 {
		t.Fatalf("expected the column default confidence, got %v", unscored.Confidence)
	}

	if _, err := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "low confidence memory", Confidence: 0.2}); err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	// The default floor hides the 0.2 row.
	byDefault, err := svc.Recall(ctx, scope, RecallRequest{NamespaceTypes: knowledge, TopK: 5})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(byDefault) != 1 || byDefault[0].Content != "unscored memory" {
		t.Fatalf("expected only the default-confidence row, got %+v", byDefault)
	}

	// The filter can be disabled, which was not expressible before.
	all, err := svc.Recall(ctx, scope, RecallRequest{NamespaceTypes: knowledge, SkipConfidenceFilter: true, TopK: 5})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both rows without the filter, got %d", len(all))
	}
}

func TestRecall_LongContentStaysSearchable(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	long := strings.Repeat("头部填充内容 ", 60) + "MIDDLETOKEN" + strings.Repeat(" 尾部填充内容", 60)
	if _, err := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: long, Confidence: 0.9}); err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	hits, err := svc.Recall(ctx, scope, RecallRequest{Query: "MIDDLETOKEN", TopK: 5})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected the long memory to be found through the substring fallback, got %d", len(hits))
	}
}

func TestRecall_LikeWildcardsAreLiteral(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "percent sign test", Confidence: 0.9})
	hits, err := svc.Recall(ctx, scope, RecallRequest{Query: "%", TopK: 5})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected a literal %% query to match nothing, got %d hits", len(hits))
	}
}

func TestRecall_TagsAndTimeFilters(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "tagged", Tags: []string{"go", "channel"}, Confidence: 0.9})
	svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "other", Tags: []string{"python"}, Confidence: 0.9})

	all, _ := svc.Recall(ctx, scope, RecallRequest{TagsAll: []string{"go", "channel"}, SkipConfidenceFilter: true, TopK: 5})
	if len(all) != 1 || all[0].Content != "tagged" {
		t.Fatalf("expected one TagsAll match, got %+v", all)
	}
	any, _ := svc.Recall(ctx, scope, RecallRequest{TagsAny: []string{"python", "go"}, SkipConfidenceFilter: true, TopK: 5})
	if len(any) != 2 {
		t.Fatalf("expected two TagsAny matches, got %d", len(any))
	}

	future := time.Now().Add(time.Hour)
	none, _ := svc.Recall(ctx, scope, RecallRequest{TimeRangeStart: &future, SkipConfidenceFilter: true, TopK: 5})
	if len(none) != 0 {
		t.Fatalf("expected the future time range to match nothing, got %d", len(none))
	}
}

// ---- Update / CAS ---------------------------------------------------------------------------

func TestUpdate_VersionedAndScoped(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	id, err := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "original", Confidence: 0.9})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	updated := "updated"
	if err := svc.Update(ctx, scope, UpdateRequest{ItemID: id, Content: &updated, ExpectedVersion: 1}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if item.Version != 2 {
		t.Fatalf("expected version 2, got %d", item.Version)
	}
	if !strings.Contains(item.TokenizedText, "updated") {
		t.Fatalf("expected the search index text to be refreshed, got %q", item.TokenizedText)
	}

	// Stale version -> conflict, and the row is untouched.
	err = svc.Update(ctx, scope, UpdateRequest{ItemID: id, Content: &updated, ExpectedVersion: 1})
	var memErr *MemoryError
	if !errors.As(err, &memErr) || memErr.Code != CodeConflict {
		t.Fatalf("expected a version conflict, got %v", err)
	}

	// Another tenant cannot reach the row, and cannot tell it apart from a missing one.
	err = svc.Update(ctx, otherTenantScope(), UpdateRequest{ItemID: id, Content: &updated, ExpectedVersion: 2})
	if !errors.As(err, &memErr) || memErr.Code != CodeNotFound {
		t.Fatalf("expected NOT_FOUND for a cross-tenant update, got %v", err)
	}

	if err := svc.Update(ctx, scope, UpdateRequest{ItemID: id, ExpectedVersion: 0}); err == nil {
		t.Fatal("expected expected_version=0 to be rejected")
	}
}

func TestUpdate_ReindexedAfterContentChange(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	id, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "用户喜欢喝茶", Confidence: 0.9})
	newContent := "用户喜欢喝燕麦拿铁"
	if err := svc.Update(ctx, scope, UpdateRequest{ItemID: id, Content: &newContent, ExpectedVersion: 1}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	hits, err := svc.Recall(ctx, scope, RecallRequest{Query: "燕麦拿铁", TopK: 5})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected the updated content to be searchable, got %d hits", len(hits))
	}
}

// ---- Forget / deletion compliance -----------------------------------------------------------

func TestForget_SoftIsReversibleAndPrunesIndex(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	id, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "to be forgotten", Confidence: 0.9})
	count, err := svc.Forget(ctx, scope, ForgetRequest{ItemIDs: []string{id}, Mode: ForgetSoft})
	if err != nil {
		t.Fatalf("forget failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 deleted row, got %d", count)
	}

	var ftsRows int64
	db.Raw("SELECT COUNT(*) FROM fts_memory WHERE item_id = ?", id).Scan(&ftsRows)
	if ftsRows != 0 {
		t.Fatalf("expected the deleted row to leave the search index, got %d", ftsRows)
	}

	hits, _ := svc.Recall(ctx, scope, RecallRequest{Query: "forgotten", TopK: 5})
	if len(hits) != 0 {
		t.Fatalf("expected a soft-deleted memory to be unrecallable, got %d", len(hits))
	}

	ok, err := svc.ValidateFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("expected ValidateFTS to stay consistent after a soft delete: ok=%v err=%v", ok, err)
	}
}

func TestForget_TargetsWholeScopeWhenTypeOmitted(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	profileID, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "profile data", Confidence: 0.9})
	transientID, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "session data", Confidence: 0.9})
	knowledgeID, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "knowledge data", Confidence: 0.9})

	// "Forget this user" must not silently stop at the transient namespace.
	count, err := svc.Forget(ctx, scope, ForgetRequest{ItemIDs: []string{profileID, transientID, knowledgeID}})
	if err != nil {
		t.Fatalf("forget failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected all three memories to be forgotten, got %d", count)
	}

	var deleted int64
	db.Model(&model.MemoryItem{}).Where("status = ?", model.ItemStatusDeleted).Count(&deleted)
	if deleted != 3 {
		t.Fatalf("expected 3 deleted rows, got %d", deleted)
	}
}

func TestForget_CannotReachAnotherTenant(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()

	victimID, _ := svc.Remember(ctx, otherTenantScope(), RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "victim data", Confidence: 0.9})

	// Even with a full namespace path in the ID list, the scope's namespace filter applies.
	count, err := svc.Forget(ctx, testScope(), ForgetRequest{ItemIDs: []string{victimID}, Mode: ForgetSoft})
	if err == nil && count != 0 {
		t.Fatalf("expected no rows to be affected, got %d", count)
	}

	var item model.MemoryItem
	db.First(&item, "id = ?", victimID)
	if item.Status != model.ItemStatusActive {
		t.Fatalf("another tenant's memory was modified: status=%q", item.Status)
	}
}

func TestForget_NotFoundAndInvalidMode(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()

	var memErr *MemoryError
	_, err := svc.Forget(ctx, testScope(), ForgetRequest{ItemIDs: []string{"missing"}, Mode: ForgetSoft})
	if !errors.As(err, &memErr) || memErr.Code != CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}

	_, err = svc.Forget(ctx, testScope(), ForgetRequest{Mode: "nuke"})
	if !errors.As(err, &memErr) || memErr.Code != CodeValidation {
		t.Fatalf("expected VALIDATION for an unknown mode, got %v", err)
	}
}

func TestForget_HardDeleteRetention(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	// Default: a recoverable copy is kept.
	backedID, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "phone 13800000000", Confidence: 0.9})
	if _, err := svc.Forget(ctx, scope, ForgetRequest{ItemIDs: []string{backedID}, Mode: ForgetHard}); err != nil {
		t.Fatalf("hard forget failed: %v", err)
	}
	var archived int64
	db.Model(&model.DeletedItem{}).Where("id = ?", backedID).Count(&archived)
	if archived != 1 {
		t.Fatalf("expected the row to be recoverable, got %d archived rows", archived)
	}

	// The recovery window is honoured, then purged.
	if purged, err := svc.PurgeExpiredDeleted(ctx, time.Now()); err != nil || purged != 0 {
		t.Fatalf("expected the retention window to protect the row, purged=%d err=%v", purged, err)
	}
	if purged, err := svc.PurgeExpiredDeleted(ctx, time.Now().Add(8*24*time.Hour)); err != nil || purged != 1 {
		t.Fatalf("expected the row to be purged after retention, purged=%d err=%v", purged, err)
	}

	// Opting out leaves nothing behind: the erasure path.
	noBackup := false
	erasedID, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "phone 13900000000", Confidence: 0.9})
	if _, err := svc.Forget(ctx, scope, ForgetRequest{ItemIDs: []string{erasedID}, Mode: ForgetHard, BackupBeforeHardDelete: &noBackup}); err != nil {
		t.Fatalf("hard forget failed: %v", err)
	}
	db.Model(&model.DeletedItem{}).Where("id = ?", erasedID).Count(&archived)
	if archived != 0 {
		t.Fatalf("expected no retained copy when backup is disabled, got %d", archived)
	}
}

func TestPurgeDeletedAndPurgeNow(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	id, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "soft deleted", Confidence: 0.9})
	if _, err := svc.Forget(ctx, scope, ForgetRequest{ItemIDs: []string{id}, Mode: ForgetSoft}); err != nil {
		t.Fatalf("forget failed: %v", err)
	}

	purged, err := svc.PurgeDeleted(ctx, time.Now().Add(time.Hour))
	if err != nil || purged != 1 {
		t.Fatalf("expected 1 purged row, got %d (err=%v)", purged, err)
	}
	var remaining int64
	db.Model(&model.MemoryItem{}).Where("id = ?", id).Count(&remaining)
	if remaining != 0 {
		t.Fatal("expected the row to be gone from memory_items")
	}

	dropped, err := svc.PurgeDeletedNow(ctx, []string{id})
	if err != nil || dropped != 1 {
		t.Fatalf("expected the archived copy to be dropped, got %d (err=%v)", dropped, err)
	}
}

func TestTouchAndRenew(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	ttl := 60
	id, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeAction, Content: "task", TTLSeconds: &ttl, Confidence: 0.9})

	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if err := svc.Touch(ctx, otherTenantScope(), id); err == nil {
		t.Fatal("expected touch from another tenant to fail")
	}
	if err := svc.Touch(ctx, scope, id); err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	db.First(&item, "id = ?", id)
	if item.AccessCount != 1 || item.LastAccessAt == nil {
		t.Fatalf("expected access stats to be recorded, got %+v", item)
	}

	original := *item.ExpiresAt
	renewed, err := svc.TouchWithRenew(ctx, scope, id, 1, 3600)
	if err != nil {
		t.Fatalf("TouchWithRenew failed: %v", err)
	}
	if !renewed {
		t.Fatal("expected the expiry to be renewed")
	}
	db.First(&item, "id = ?", id)
	if !item.ExpiresAt.After(original) {
		t.Fatalf("expected a later expiry, got %v (was %v)", item.ExpiresAt, original)
	}

	if err := svc.RenewExpiration(ctx, scope, id, 7200); err != nil {
		t.Fatalf("RenewExpiration failed: %v", err)
	}
	if err := svc.RenewExpiration(ctx, otherTenantScope(), id, 7200); err == nil {
		t.Fatal("expected renewal from another tenant to fail")
	}
}

func TestCleanupExpiredPrunesIndexAndCallbacks(t *testing.T) {
	db := store.SetupTestDB(t)
	var expired []string
	svc := NewMemoryService(db).WithConfig(Config{
		OnExpired: func(_ context.Context, id string) { expired = append(expired, id) },
	})
	ctx := context.Background()
	scope := testScope()

	ttl := 1
	id, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "expiring", TTLSeconds: &ttl, Confidence: 0.9})
	if err := db.Model(&model.MemoryItem{}).Where("id = ?", id).Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("forcing expiry failed: %v", err)
	}

	count, err := svc.CleanupExpired(ctx)
	if err != nil || count != 1 {
		t.Fatalf("expected 1 expired row, got %d (err=%v)", count, err)
	}
	if len(expired) != 1 || expired[0] != id {
		t.Fatalf("expected the expiry callback, got %v", expired)
	}

	var ftsRows int64
	db.Raw("SELECT COUNT(*) FROM fts_memory WHERE item_id = ?", id).Scan(&ftsRows)
	if ftsRows != 0 {
		t.Fatalf("expected the expired row to leave the index, got %d", ftsRows)
	}
	ok, _ := svc.ValidateFTS(ctx)
	if !ok {
		t.Fatal("expected the index to stay consistent")
	}
}

// ---- Indexing -------------------------------------------------------------------------------

func TestSearchIndexFollowsStatusTransitions(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	id, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "status juggling", Confidence: 0.9})
	countIndexed := func() int64 {
		var n int64
		db.Raw("SELECT COUNT(*) FROM fts_memory WHERE item_id = ?", id).Scan(&n)
		return n
	}
	if countIndexed() != 1 {
		t.Fatal("expected an active row to be indexed")
	}

	// A status change made outside the service (a maintenance script, a manual UPDATE) must not
	// leave a stale index entry behind: the triggers own that invariant.
	for _, status := range []model.ItemStatus{model.ItemStatusExpired, model.ItemStatusArchived, model.ItemStatusDeleted, model.ItemStatusActive} {
		if err := db.Exec("UPDATE memory_items SET status = ? WHERE id = ?", status, id).Error; err != nil {
			t.Fatalf("status update failed: %v", err)
		}
		want := int64(0)
		if status == model.ItemStatusActive {
			want = 1
		}
		if got := countIndexed(); got != want {
			t.Fatalf("status %q: expected %d index rows, got %d", status, want, got)
		}
	}
}

func TestRebuildFTSAndValidate(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "golang 并发 编程", Confidence: 0.9})
	if err := db.Exec("DELETE FROM fts_memory").Error; err != nil {
		t.Fatalf("corrupting the index failed: %v", err)
	}
	if ok, err := svc.ValidateFTS(ctx); err != nil || ok {
		t.Fatalf("expected drift to be detected: ok=%v err=%v", ok, err)
	}
	if err := svc.RebuildFTS(ctx); err != nil {
		t.Fatalf("RebuildFTS failed: %v", err)
	}
	if ok, err := svc.ValidateFTS(ctx); err != nil || !ok {
		t.Fatalf("expected a consistent index after rebuild: ok=%v err=%v", ok, err)
	}
	hits, _ := svc.Recall(ctx, scope, RecallRequest{Query: "golang", TopK: 5})
	if len(hits) != 1 {
		t.Fatalf("expected the rebuilt index to serve the query, got %d hits", len(hits))
	}
}

// ---- Policy ---------------------------------------------------------------------------------

func TestPolicy_PrefixMatchAndDefaults(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()
	pm := NewPolicyManager(svc.db)

	// Wildcard rules must apply to their subtree.
	if err := pm.SetPolicy(ctx, model.NamespacePolicy{
		Namespace:       "knowledge/projA/*",
		TTLSeconds:      intPtr(60),
		RankWeightsJSON: `{"fts":1,"recency":0,"importance":0,"confidence":0}`,
	}); err != nil {
		t.Fatalf("SetPolicy failed: %v", err)
	}
	policy, err := pm.GetPolicy(ctx, "knowledge/projA/123")
	if err != nil {
		t.Fatalf("GetPolicy failed: %v", err)
	}
	if policy.Namespace != "knowledge/projA/*" {
		t.Fatalf("expected the wildcard rule, got %q", policy.Namespace)
	}

	// The most specific rule wins.
	if err := pm.SetPolicy(ctx, model.NamespacePolicy{Namespace: "knowledge/projA/vip", TTLSeconds: intPtr(3600)}); err != nil {
		t.Fatalf("SetPolicy failed: %v", err)
	}
	policy, _ = pm.GetPolicy(ctx, "knowledge/projA/vip")
	if policy.Namespace != "knowledge/projA/vip" {
		t.Fatalf("expected the exact rule to win, got %q", policy.Namespace)
	}

	// Isolated namespaces resolve to their own type's default policy.
	knowledge, _ := pm.GetPolicy(ctx, scope.Namespace(model.NamespaceTypeKnowledge))
	transient, _ := pm.GetPolicy(ctx, scope.Namespace(model.NamespaceTypeTransient))
	if knowledge.TTLSeconds != nil {
		t.Fatal("expected knowledge to default to no expiry")
	}
	if transient.TTLSeconds == nil {
		t.Fatal("expected transient to default to a TTL")
	}
	if knowledge.RankWeightsJSON == transient.RankWeightsJSON {
		t.Fatal("expected distinct default rank weights per namespace type")
	}
}

func TestPolicy_RankWeightsDriveRecall(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	// Rank purely by importance in this namespace.
	if err := NewPolicyManager(svc.db).SetPolicy(ctx, model.NamespacePolicy{
		Namespace:       scope.Namespace(model.NamespaceTypeKnowledge),
		RankWeightsJSON: `{"fts":0,"recency":0,"importance":1,"confidence":0}`,
	}); err != nil {
		t.Fatalf("SetPolicy failed: %v", err)
	}

	lowID, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "same query text", Importance: 10, Confidence: 0.9})
	highID, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "same query text", Importance: 90, Confidence: 0.9})

	hits, err := svc.Recall(ctx, scope, RecallRequest{Query: "same query text", TopK: 2})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].ID != highID || hits[1].ID != lowID {
		t.Fatalf("expected importance ordering %s,%s got %s,%s", highID, lowID, hits[0].ID, hits[1].ID)
	}
}

// ---- Decision engine ------------------------------------------------------------------------

func TestDecision_UpdateKeepsIndexAndVersion(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()
	de := NewDecisionEngine(db)

	id, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "用户喜欢喝茶", Confidence: 0.9})

	result, err := de.ExecuteDecisions(ctx, scope,
		[]ExtractedMemory{{TempID: "c1", Namespace: model.NamespaceTypeProfile, Content: "用户喜欢喝燕麦拿铁", Confidence: 0.9}},
		[]MemoryDecision{{Decision: DecisionUpdate, TargetID: id, Confidence: 0.9}})
	if err != nil {
		t.Fatalf("ExecuteDecisions failed: %v", err)
	}
	if len(result.Updated) != 1 || len(result.Errors) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if item.Version != 2 {
		t.Fatalf("expected the decision engine to bump the version, got %d", item.Version)
	}
	hits, _ := svc.Recall(ctx, scope, RecallRequest{Query: "燕麦拿铁", TopK: 5})
	if len(hits) != 1 {
		t.Fatalf("expected the merged content to be searchable, got %d hits", len(hits))
	}
}

func TestDecision_DeleteIsSoftAndPrunesIndex(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()
	de := NewDecisionEngine(db)

	id, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "contradicted fact", Confidence: 0.9})
	result, err := de.ExecuteDecisions(ctx, scope,
		[]ExtractedMemory{{TempID: "d1", Namespace: model.NamespaceTypeProfile, Content: "replacement fact", Confidence: 0.9}},
		[]MemoryDecision{{Decision: DecisionDelete, TargetID: id, Reason: "superseded"}})
	if err != nil {
		t.Fatalf("ExecuteDecisions failed: %v", err)
	}
	if len(result.Deleted) != 1 {
		t.Fatalf("expected one deletion, got %+v", result)
	}

	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if item.Status != model.ItemStatusDeleted {
		t.Fatalf("expected a reversible soft delete, got status %q", item.Status)
	}
	var ftsRows int64
	db.Raw("SELECT COUNT(*) FROM fts_memory WHERE item_id = ?", id).Scan(&ftsRows)
	if ftsRows != 0 {
		t.Fatalf("expected the index to be pruned, got %d rows", ftsRows)
	}
}

func TestDecision_CannotReachOutsideScopeOrNamespace(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	de := NewDecisionEngine(db)

	// A memory in another tenant, and one in a different namespace of the same scope.
	victimID, _ := svc.Remember(ctx, otherTenantScope(), RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "victim", Confidence: 0.9})
	knowledgeID, _ := svc.Remember(ctx, testScope(), RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "knowledge", Confidence: 0.9})

	for _, target := range []string{victimID, knowledgeID} {
		candidate := ExtractedMemory{TempID: "x", Namespace: model.NamespaceTypeProfile, Content: "hijack", Confidence: 0.9}
		result, err := de.ExecuteDecisions(ctx, testScope(),
			[]ExtractedMemory{candidate},
			[]MemoryDecision{{Decision: DecisionDelete, TargetID: target}})
		if err != nil {
			t.Fatalf("ExecuteDecisions failed: %v", err)
		}
		if len(result.Deleted) != 0 || len(result.Errors) == 0 {
			t.Fatalf("expected target %s to be refused, got %+v", target, result)
		}
	}

	var victim, knowledge model.MemoryItem
	db.First(&victim, "id = ?", victimID)
	db.First(&knowledge, "id = ?", knowledgeID)
	if victim.Status != model.ItemStatusActive || knowledge.Status != model.ItemStatusActive {
		t.Fatal("a decision escaped its scope or namespace")
	}
}

func TestDecision_MergeCombinesAndRecords(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()
	de := NewDecisionEngine(db)

	id, _ := svc.Remember(ctx, scope, RememberRequest{
		NamespaceType: model.NamespaceTypeProfile, Content: "用户喜欢喝茶", Tags: []string{"drink"}, Importance: 40, Confidence: 0.8,
	})

	result, err := de.ExecuteDecisions(ctx, scope,
		[]ExtractedMemory{{TempID: "m1", Namespace: model.NamespaceTypeProfile, Content: "用户也喜欢喝咖啡", Tags: []string{"coffee"}, Confidence: 0.9}},
		[]MemoryDecision{{Decision: DecisionMerge, TargetID: id, NewImportance: 70}})
	if err != nil {
		t.Fatalf("ExecuteDecisions failed: %v", err)
	}
	if len(result.Merged) != 1 {
		t.Fatalf("expected a merge, got %+v", result)
	}

	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if !strings.Contains(item.Content, "喝茶") || !strings.Contains(item.Content, "咖啡") {
		t.Fatalf("expected both facts to survive the merge, got %q", item.Content)
	}
	if item.Importance != 70 {
		t.Fatalf("expected the new importance, got %d", item.Importance)
	}

	var merges []model.MemoryMerge
	db.Find(&merges)
	if len(merges) != 1 || merges[0].TargetID != id {
		t.Fatalf("expected a merge record, got %+v", merges)
	}
}

func TestDecision_UnknownDecisionIsReportedNotAdded(t *testing.T) {
	_, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()
	de := NewDecisionEngine(db)

	result, err := de.ExecuteDecisions(ctx, scope,
		[]ExtractedMemory{{TempID: "u1", Namespace: model.NamespaceTypeProfile, Content: "content", Confidence: 0.9}},
		[]MemoryDecision{{Decision: "BOGUS"}})
	if err != nil {
		t.Fatalf("ExecuteDecisions failed: %v", err)
	}
	if len(result.Errors) != 1 || len(result.Added) != 0 {
		t.Fatalf("expected the unknown decision to be reported and skipped, got %+v", result)
	}

	var count int64
	db.Model(&model.MemoryItem{}).Count(&count)
	if count != 0 {
		t.Fatalf("expected nothing to be written, got %d rows", count)
	}
}

func TestDecision_FindSimilarMemoriesIsScoped(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	de := NewDecisionEngine(db)

	svc.Remember(ctx, testScope(), RememberRequest{NamespaceType: model.NamespaceTypeTransient, Title: "Go channel notes", Content: "Go channel best practices", Tags: []string{"go", "channel"}, Confidence: 0.9})
	svc.Remember(ctx, otherSessionScope(), RememberRequest{NamespaceType: model.NamespaceTypeTransient, Title: "Go channel notes", Content: "Go channel best practices", Tags: []string{"go", "channel"}, Confidence: 0.9})
	svc.Remember(ctx, otherTenantScope(), RememberRequest{NamespaceType: model.NamespaceTypeTransient, Title: "Go channel notes", Content: "Go channel best practices", Tags: []string{"go", "channel"}, Confidence: 0.9})

	candidate := ExtractedMemory{Namespace: model.NamespaceTypeTransient, Title: "Go channel", Content: "channel best practices", Tags: []string{"go", "channel"}}
	similar, err := de.FindSimilarMemories(ctx, testScope(), candidate, 10)
	if err != nil {
		t.Fatalf("FindSimilarMemories failed: %v", err)
	}
	if len(similar) != 1 {
		t.Fatalf("expected exactly the caller's own memory, got %d", len(similar))
	}
	if similar[0].Similarity <= 0 || similar[0].Similarity > 1 {
		t.Fatalf("expected a similarity in (0,1], got %v", similar[0].Similarity)
	}
}

// ---- Extractor ------------------------------------------------------------------------------

func TestExtractor_CacheIsScopedAndDryRunDoesNotPoison(t *testing.T) {
	db := store.SetupTestDB(t)
	dialog := "用户：我的手机号是 13800000000"
	contextHash := calculateHash(dialog)
	tenantA, tenantB := testScope(), otherTenantScope()

	// A completed extraction of tenant A must not be visible to tenant B.
	if err := db.Create(&model.DialogExtraction{
		ID: "exA", ScopeKey: tenantA.String(), DialogText: dialog, DialogHash: contextHash,
		ExtractedMemoriesJSON: `[{"namespace":"profile","content":"tenant A private","confidence":0.95}]`,
		Status:                model.ExtractionStatusCompleted, CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seeding failed: %v", err)
	}
	if tenantA.String() == tenantB.String() {
		t.Fatal("expected distinct scope keys")
	}

	var found model.DialogExtraction
	err := db.Where("scope_key = ? AND dialog_hash = ? AND status = ?",
		tenantB.String(), contextHash, model.ExtractionStatusCompleted).First(&found).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("tenant B must not see tenant A's extraction, got %v (%+v)", err, found)
	}

	// A dry-run probe is recorded as pending, so it cannot satisfy a later real write.
	if err := db.Create(&model.DialogExtraction{
		ID: "dry", ScopeKey: tenantB.String(), DialogText: dialog, DialogHash: contextHash,
		ExtractedMemoriesJSON: `[{"namespace":"profile","content":"dry run only","confidence":0.95}]`,
		Status:                model.ExtractionStatusPending, CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seeding failed: %v", err)
	}
	err = db.Where("scope_key = ? AND dialog_hash = ? AND status = ?",
		tenantB.String(), contextHash, model.ExtractionStatusCompleted).First(&model.DialogExtraction{}).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("a dry run must not populate the completed cache, got %v", err)
	}
}

func TestExtractor_PersistIsIdempotent(t *testing.T) {
	db := store.SetupTestDB(t)
	e := NewExtractor(db)
	ctx := context.Background()
	scope := testScope()

	mem := ExtractedMemory{Namespace: model.NamespaceTypeProfile, Title: "偏好", Content: "用户偏好中文回复", Confidence: 0.95}
	for i := 0; i < 3; i++ {
		if err := e.persistMemory(ctx, scope, mem); err != nil {
			t.Fatalf("persist %d failed: %v", i, err)
		}
	}

	var count int64
	db.Model(&model.MemoryItem{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected a single row from repeated persistence, got %d", count)
	}
}

func TestExtractPolicy_PostPipeline(t *testing.T) {
	// The confidence gate, the transient-ephemeral filter and the host hook compose in order.
	memories := []ExtractedMemory{
		{Namespace: model.NamespaceTypeProfile, Content: "keep me", Confidence: 0.9},
		{Namespace: model.NamespaceTypeProfile, Content: "drop me", Confidence: 0.3},
		{Namespace: model.NamespaceTypeTransient, Content: "当前时间是 12:00", Confidence: 0.9},
	}
	filtered, err := applyPostExtractPipeline(context.Background(), memories, ExtractRequest{
		MinConfidence: 0.7,
		ExtractPolicy: &ExtractPolicy{DropTransientEphemeral: true},
	})
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Content != "keep me" {
		t.Fatalf("unexpected pipeline output: %+v", filtered)
	}

	hooked, err := applyPostExtractPipeline(context.Background(), memories, ExtractRequest{
		MinConfidence: 0.7,
		PostExtractHook: func(_ context.Context, mem []ExtractedMemory) ([]ExtractedMemory, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(hooked) != 0 {
		t.Fatalf("expected the hook to win, got %+v", hooked)
	}
}

// ---- Isolation guarantees -------------------------------------------------------------------

func TestNoOperationLeaksAcrossScopes(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	owner, intruder := testScope(), otherTenantScope()

	id, _ := svc.Remember(ctx, owner, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "secret", Confidence: 0.9})

	hits, err := svc.Recall(ctx, intruder, RecallRequest{Query: "secret", TopK: 5})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("recall leaked %d memories across tenants", len(hits))
	}

	items, err := svc.List(ctx, intruder, ListRequest{TopK: 5})
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("list leaked %d memories across tenants", len(items))
	}

	if err := svc.Touch(ctx, intruder, id); err == nil {
		t.Fatal("touch leaked across tenants")
	}
	if err := svc.RenewExpiration(ctx, intruder, id, 60); err == nil {
		t.Fatal("renewal leaked across tenants")
	}
	if _, err := svc.Forget(ctx, intruder, ForgetRequest{ItemIDs: []string{id}, Mode: ForgetSoft}); err == nil {
		t.Fatal("forget leaked across tenants")
	}

	var item model.MemoryItem
	svc.db.First(&item, "id = ?", id)
	if item.Status != model.ItemStatusActive {
		t.Fatal("the intruder managed to modify another tenant's memory")
	}
}

// ---- Ops ------------------------------------------------------------------------------------

func TestStatsHealthAndEvents(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db).WithConfig(Config{RecordEvents: true})
	ctx := WithActor(WithTrace(context.Background(), "trace-1", "req-1"), "agent-1")
	scope := testScope()

	id, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "audited", Confidence: 0.9})
	if _, err := svc.Forget(ctx, scope, ForgetRequest{ItemIDs: []string{id}, Mode: ForgetSoft}); err != nil {
		t.Fatalf("forget failed: %v", err)
	}

	var events []model.MemoryEvent
	if err := db.Order("created_at").Find(&events).Error; err != nil {
		t.Fatalf("load events failed: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected create+delete audit rows, got %d", len(events))
	}
	if events[0].Actor == nil || *events[0].Actor != "agent-1" {
		t.Fatalf("expected the actor on the audit row, got %+v", events[0])
	}
	if events[0].TraceID == nil || *events[0].TraceID != "trace-1" {
		t.Fatalf("expected the trace id on the audit row, got %+v", events[0])
	}

	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats.SoftDeletedItems != 1 || !stats.IndexInSync || stats.Events != int64(len(events)) {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if err := svc.Health(ctx); err != nil {
		t.Fatalf("Health failed: %v", err)
	}
}

func TestJanitorSweepsExpiryAndRetention(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()

	ttl := 1
	id, _ := svc.Remember(ctx, testScope(), RememberRequest{NamespaceType: model.NamespaceTypeTransient, Content: "expiring", TTLSeconds: &ttl, Confidence: 0.9})
	if err := db.Model(&model.MemoryItem{}).Where("id = ?", id).Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("forcing expiry failed: %v", err)
	}
	if err := db.Create(&model.DeletedItem{
		ID: "old-archive", OriginalDataJSON: "{}",
		DeletedAt:  time.Now().Add(-48 * time.Hour),
		PurgeAfter: time.Now().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seeding the archive failed: %v", err)
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
	db.Model(&model.DeletedItem{}).Where("id = ?", "old-archive").Count(&archived)
	if archived != 0 {
		t.Fatalf("expected the expired archive copy to be purged, got %d", archived)
	}
}

func TestJanitorStartStop(t *testing.T) {
	svc, _ := newSvc(t)
	janitor := NewJanitor(svc, JanitorConfig{Interval: 10 * time.Millisecond})
	janitor.Start(context.Background())
	time.Sleep(50 * time.Millisecond)
	janitor.Stop()
}

func TestConfig_StoreDefaults(t *testing.T) {
	cfg := store.DefaultConfig()
	if cfg.MaxOpenConns != 1 {
		t.Fatalf("expected a single-connection pool by default, got %d", cfg.MaxOpenConns)
	}
	if cfg.BusyTimeoutMilliseconds <= 0 {
		t.Fatal("expected a busy timeout by default")
	}
}

// ---- Token accounting -----------------------------------------------------------------------

func TestTokenCounter_EstimatesConservative(t *testing.T) {
	if got := CountTokens(""); got != 0 {
		t.Fatalf("expected 0 tokens for empty text, got %d", got)
	}
	if got := CountTokens("你好世界"); got != 4 {
		t.Fatalf("expected 4 tokens for 4 CJK runes, got %d", got)
	}
	// Latin text is charged at ~4 characters per token, rounded up ("hello" + "world" = 2 + 2).
	if got := CountTokens("hello world"); got != 4 {
		t.Fatalf("expected 4 tokens for 'hello world', got %d", got)
	}
	if got := CountTokens("codegraph init --workspace"); got == 0 {
		t.Fatal("expected a positive estimate for mixed text")
	}

	// Trimming never exceeds the budget and never splits a rune.
	text := strings.Repeat("重要信息 ", 20)
	trimmed := trimToTokens(text, 10)
	if CountTokens(trimmed) > 10 {
		t.Fatalf("trim exceeded the budget: %q", trimmed)
	}
	if trimmed == "" || !strings.HasPrefix(text, trimmed) {
		t.Fatalf("expected a prefix of the input, got %q", trimmed)
	}
	if strings.Contains(trimmed, "\uFFFD") {
		t.Fatal("trimming split a multi-byte character")
	}

	// The counter is injectable so hosts can plug in an exact tokenizer.
	SetTokenCounter(func(string) int { return 1 })
	defer SetTokenCounter(nil)
	if got := CountTokens("anything"); got != 1 {
		t.Fatalf("expected the injected counter, got %d", got)
	}
	if got := CountTokens("anything"); got != 1 {
		t.Fatalf("expected the injected counter to persist, got %d", got)
	}
	if got := CountTokens(strings.Repeat("x", 1000)); got != 1 {
		t.Fatalf("expected the injected counter to be used after reset, got %d", got)
	}
	SetTokenCounter(nil)
	if got := CountTokens("你好"); got != 2 {
		t.Fatalf("expected the default counter after reset, got %d", got)
	}
}

// ---- Context assembly -----------------------------------------------------------------------

func TestRecallContext_FitsBudgetAndReportsTruncation(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	// Eight memories, each long enough that they cannot all fit.
	for i := 0; i < 8; i++ {
		if _, err := svc.Remember(ctx, scope, RememberRequest{
			NamespaceType: model.NamespaceTypeKnowledge,
			Title:         "topic " + itoa(i),
			Content:       strings.Repeat("记忆内容细节 ", 30),
			Confidence:    0.9,
		}); err != nil {
			t.Fatalf("remember failed: %v", err)
		}
	}

	bundle, err := svc.RecallContext(ctx, scope, RecallRequest{NamespaceTypes: []model.NamespaceType{model.NamespaceTypeKnowledge}}, RecallBudget{
		MaxTokens: 120,
	})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if bundle.Tokens > 120 {
		t.Fatalf("bundle exceeds the budget: %d tokens", bundle.Tokens)
	}
	if bundle.Tokens != CountTokens(bundle.Text) {
		t.Fatalf("reported tokens (%d) disagree with the assembled text (%d)", bundle.Tokens, CountTokens(bundle.Text))
	}
	if bundle.Text == "" {
		t.Fatal("expected assembled text")
	}
	if !bundle.Truncated {
		t.Fatal("expected the budget to report truncation")
	}
	if bundle.Candidates != 8 {
		t.Fatalf("expected all 8 memories to be considered, got %d", bundle.Candidates)
	}
	if len(bundle.Items) == 0 {
		t.Fatal("expected at least one bundled memory")
	}

	sum := 0
	for _, item := range bundle.Items {
		sum += item.Tokens
		if item.ID == "" || item.Text == "" {
			t.Fatalf("incomplete bundle item: %+v", item)
		}
	}
	if sum > 120 {
		t.Fatalf("items exceed the budget: %d", sum)
	}
}

func TestRecallContext_MaxItemsAndPerItemCap(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()
	knowledge := []model.NamespaceType{model.NamespaceTypeKnowledge}

	for i := 0; i < 5; i++ {
		svc.Remember(ctx, scope, RememberRequest{
			NamespaceType: model.NamespaceTypeKnowledge,
			Title:         "item " + itoa(i),
			Content:       strings.Repeat("内容内容内容 ", 40),
			Importance:    100 - i,
			Confidence:    0.9,
		})
	}

	bundle, err := svc.RecallContext(ctx, scope, RecallRequest{NamespaceTypes: knowledge}, RecallBudget{
		MaxItems:         2,
		PerItemMaxTokens: 40,
	})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if len(bundle.Items) != 2 {
		t.Fatalf("expected exactly 2 items, got %d", len(bundle.Items))
	}
	for _, item := range bundle.Items {
		// PerItemMaxTokens bounds the memory text; Tokens is the rendered cost (bullet + marker).
		if CountTokens(item.Text) > 40 {
			t.Fatalf("per-item cap violated: text is %d tokens", CountTokens(item.Text))
		}
		if !item.Truncated {
			t.Fatalf("expected the long memory to be marked truncated: %+v", item)
		}
	}

	// An unbudgeted bundle keeps every candidate and reports no truncation.
	full, err := svc.RecallContext(ctx, scope, RecallRequest{NamespaceTypes: knowledge, TopK: 5}, RecallBudget{})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if len(full.Items) != 5 || full.Truncated {
		t.Fatalf("expected a complete, untruncated bundle, got %d items truncated=%v", len(full.Items), full.Truncated)
	}
}

func TestRecallContext_EmptyResult(t *testing.T) {
	svc, _ := newSvc(t)
	bundle, err := svc.RecallContext(context.Background(), testScope(), RecallRequest{Query: "nothing matches this"}, RecallBudget{MaxTokens: 200})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if bundle.Text != "" || bundle.Tokens != 0 || len(bundle.Items) != 0 || bundle.Candidates != 0 {
		t.Fatalf("expected an empty bundle, got %+v", bundle)
	}
}

func TestRecallContext_FormalizerAndSummaryText(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	svc.Remember(ctx, scope, RememberRequest{
		NamespaceType: model.NamespaceTypeProfile,
		Title:         "Theme",
		Content:       "The user prefers a dark theme, and dislikes bright surfaces.",
		Summary:       "prefers dark theme",
		Confidence:    0.9,
	})

	bundle, err := svc.RecallContext(ctx, scope, RecallRequest{NamespaceTypes: []model.NamespaceType{model.NamespaceTypeProfile}}, RecallBudget{
		IncludeSummaryText: true,
		MaxTokens:          50,
	})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if len(bundle.Items) != 1 || !strings.Contains(bundle.Items[0].Text, "prefers dark theme") {
		t.Fatalf("expected the summary to be preferred, got %+v", bundle.Items)
	}
	if strings.Contains(bundle.Items[0].Text, "bright surfaces") {
		t.Fatal("expected the full content to be replaced by the summary")
	}

	custom, err := svc.RecallContext(ctx, scope, RecallRequest{NamespaceTypes: []model.NamespaceType{model.NamespaceTypeProfile}}, RecallBudget{
		Formalizer: func(_ context.Context, hits []MemoryHit) string {
			return "custom(" + itoa(len(hits)) + ")"
		},
	})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if custom.Text != "custom(1)" {
		t.Fatalf("expected the custom formalizer output, got %q", custom.Text)
	}
}

// ---- Namespace digests ----------------------------------------------------------------------

func TestNamespaceSummary_RefreshCacheAndPolicy(t *testing.T) {
	db := store.SetupTestDB(t)
	svc := NewMemoryService(db)
	ctx := context.Background()
	scope := testScope()
	knowledgeNS := scope.Namespace(model.NamespaceTypeKnowledge)

	for i := 0; i < 3; i++ {
		if _, err := svc.Remember(ctx, scope, RememberRequest{
			NamespaceType: model.NamespaceTypeKnowledge,
			Title:         "topic " + itoa(i),
			Content:       strings.Repeat("细节 ", 60),
			Tags:          []string{"go", "sqlite"},
			Importance:    90 - i,
			Confidence:    0.9,
		}); err != nil {
			t.Fatalf("remember failed: %v", err)
		}
	}

	digest, err := svc.RefreshNamespaceSummary(ctx, scope, knowledgeNS)
	if err != nil {
		t.Fatalf("RefreshNamespaceSummary failed: %v", err)
	}
	if digest.ItemCount != 3 {
		t.Fatalf("expected 3 items in the digest, got %d", digest.ItemCount)
	}
	if !strings.Contains(digest.Summary, "3 memories") {
		t.Fatalf("expected a count line in the digest, got %q", digest.Summary)
	}
	if !strings.Contains(digest.Summary, "topics:") {
		t.Fatalf("expected the tag rollup in the digest, got %q", digest.Summary)
	}
	if !digest.Stale {
		t.Fatal("expected the first computation to be reported as freshly computed")
	}

	// Second call within the freshness window reuses the cached digest.
	cached, err := svc.RefreshNamespaceSummary(ctx, scope, knowledgeNS)
	if err != nil {
		t.Fatalf("RefreshNamespaceSummary failed: %v", err)
	}
	if cached.Stale {
		t.Fatal("expected the cached digest to be reused")
	}
	if cached.Summary != digest.Summary {
		t.Fatalf("cached digest differs:\n got %q\nwant %q", cached.Summary, digest.Summary)
	}

	// A new memory invalidates it.
	if _, err := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Title: "new topic", Content: "new content", Confidence: 0.9}); err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	refreshed, err := svc.RefreshNamespaceSummary(ctx, scope, knowledgeNS)
	if err != nil {
		t.Fatalf("RefreshNamespaceSummary failed: %v", err)
	}
	if !refreshed.Stale || refreshed.ItemCount != 4 {
		t.Fatalf("expected the digest to be recomputed, got stale=%v count=%d", refreshed.Stale, refreshed.ItemCount)
	}

	// SummaryEnabled=false (policy) switches the digest off.
	if err := NewPolicyManager(db).SetPolicy(ctx, model.NamespacePolicy{Namespace: knowledgeNS, SummaryEnabled: false}); err != nil {
		t.Fatalf("SetPolicy failed: %v", err)
	}
	if err := db.Model(&model.NamespaceSummary{}).Where("namespace = ?", knowledgeNS).Update("updated_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("aging the cache failed: %v", err)
	}
	disabled, err := svc.RefreshNamespaceSummary(ctx, scope, knowledgeNS)
	if err != nil {
		t.Fatalf("RefreshNamespaceSummary failed: %v", err)
	}
	if disabled.Summary != "" {
		t.Fatalf("expected summaries to be disabled by policy, got %q", disabled.Summary)
	}
	if disabled.ItemCount != 4 {
		t.Fatalf("expected the item count to remain available, got %d", disabled.ItemCount)
	}
}

func TestNamespaceSummary_GenerateExplicitAndScopeGuard(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()
	profileNS := scope.Namespace(model.NamespaceTypeProfile)

	// Nothing to summarise yet.
	if _, err := svc.GenerateNamespaceSummary(ctx, scope, profileNS); err == nil {
		t.Fatal("expected an error for an empty namespace")
	}

	svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "prefers dark theme", Confidence: 0.9})

	text, err := svc.GenerateNamespaceSummary(ctx, scope, profileNS)
	if err != nil {
		t.Fatalf("GenerateNamespaceSummary failed: %v", err)
	}
	if !strings.Contains(text, "prefers dark theme") {
		t.Fatalf("expected the memory to appear in the digest, got %q", text)
	}

	// A namespace outside the scope cannot be summarised, and the caller learns nothing about it.
	var memErr *MemoryError
	if _, err := svc.GenerateNamespaceSummary(ctx, testScope(), otherTenantScope().Namespace(model.NamespaceTypeProfile)); !errors.As(err, &memErr) || memErr.Code != CodeUnauthorized {
		t.Fatalf("expected UNAUTHORIZED for a foreign namespace, got %v", err)
	}
	if _, err := svc.RefreshNamespaceSummary(ctx, testScope(), "profile/default"); !errors.As(err, &memErr) || memErr.Code != CodeUnauthorized {
		t.Fatalf("expected UNAUTHORIZED for the global namespace in scoped mode, got %v", err)
	}
}

func TestRecallContext_IncludesNamespaceSummaryWithinBudget(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	for i := 0; i < 4; i++ {
		svc.Remember(ctx, scope, RememberRequest{
			NamespaceType: model.NamespaceTypeKnowledge,
			Title:         "主题" + itoa(i),
			Content:       strings.Repeat("要点内容 ", 40),
			Tags:          []string{"go"},
			Confidence:    0.9,
		})
	}

	bundle, err := svc.RecallContext(ctx, scope, RecallRequest{NamespaceTypes: []model.NamespaceType{model.NamespaceTypeKnowledge}}, RecallBudget{
		MaxItems:                  1,
		IncludeNamespaceSummary:   true,
		NamespaceSummaryMaxTokens: 200,
		MaxTokens:                 400,
	})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if len(bundle.NamespaceSummaries) != 1 {
		t.Fatalf("expected one namespace digest, got %d", len(bundle.NamespaceSummaries))
	}
	if !strings.Contains(bundle.Text, "Namespace:") {
		t.Fatalf("expected the digest in the assembled text, got %q", bundle.Text)
	}
	if bundle.Tokens > 400 {
		t.Fatalf("bundle exceeds the budget: %d", bundle.Tokens)
	}
	// The digest itself honours its own cap.
	if bundle.NamespaceSummaries[0].Tokens > 200 {
		t.Fatalf("digest exceeds its budget: %d", bundle.NamespaceSummaries[0].Tokens)
	}
}

// ---- Item summaries -------------------------------------------------------------------------

func TestGenerateItemSummary_DerivesFromContent(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	id, err := svc.Remember(ctx, scope, RememberRequest{
		NamespaceType: model.NamespaceTypeKnowledge,
		Title:         "SQLite 并发",
		Content:       "SQLite 使用 WAL 支持并发读。写操作是串行的，需要 busy_timeout。第三句不应该出现在摘要里，因为它超出了预算。",
		Confidence:    0.9,
	})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	gen := NewSummaryGenerator(db)
	if err := gen.GenerateItemSummary(ctx, scope, id, 25); err != nil {
		t.Fatalf("GenerateItemSummary failed: %v", err)
	}

	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if item.Summary == "" {
		t.Fatal("expected a derived summary")
	}
	if CountTokens(item.Summary) > 25 {
		t.Fatalf("summary exceeds the budget: %q (%d tokens)", item.Summary, CountTokens(item.Summary))
	}
	if !strings.Contains(item.Summary, "SQLite") {
		t.Fatalf("expected the subject to survive summarisation, got %q", item.Summary)
	}
	if strings.Contains(item.Summary, "第三句") {
		t.Fatalf("expected the summary to stop at a sentence boundary, got %q", item.Summary)
	}

	// A summary generated for another tenant's memory must not be reachable.
	var memErr *MemoryError
	if err := gen.GenerateItemSummary(ctx, otherTenantScope(), id, 25); !errors.As(err, &memErr) || memErr.Code != CodeNotFound {
		t.Fatalf("expected NOT_FOUND for a foreign memory, got %v", err)
	}

	// Short content is summarised verbatim.
	shortID, _ := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeProfile, Content: "用户偏好中文回复", Confidence: 0.9})
	if err := gen.GenerateItemSummary(ctx, scope, shortID, 60); err != nil {
		t.Fatalf("GenerateItemSummary failed: %v", err)
	}
	var short model.MemoryItem
	db.First(&short, "id = ?", shortID)
	if !strings.Contains(short.Summary, "用户偏好中文回复") {
		t.Fatalf("expected short content to be kept intact, got %q", short.Summary)
	}
}

// ---- Policy persistence ---------------------------------------------------------------------

func TestSetPolicy_PersistsZeroValuedFields(t *testing.T) {
	db := store.SetupTestDB(t)
	ctx := context.Background()
	pm := NewPolicyManager(db)
	namespace := testScope().Namespace(model.NamespaceTypeKnowledge)

	// The scope-bound baseline targets the namespace this scope actually writes to.
	policy := testScope().Policy(model.NamespaceTypeKnowledge)
	if policy.Namespace != namespace {
		t.Fatalf("expected the policy to be bound to %q, got %q", namespace, policy.Namespace)
	}

	// A policy whose meaning is carried by zero values: summaries off, no TTL, no sliding renewal.
	policy.SummaryEnabled = false
	policy.SlidingTTLThreshold = 0
	policy.TTLSeconds = nil
	if err := pm.SetPolicy(ctx, policy); err != nil {
		t.Fatalf("SetPolicy failed: %v", err)
	}

	resolved, err := pm.GetPolicy(ctx, namespace)
	if err != nil {
		t.Fatalf("GetPolicy failed: %v", err)
	}
	if resolved.SummaryEnabled {
		t.Fatal("SummaryEnabled=false was not persisted (zero values must survive a write)")
	}
	if resolved.TTLSeconds != nil {
		t.Fatalf("expected no TTL, got %v", *resolved.TTLSeconds)
	}

	// Explicitly turning it back on also round-trips.
	policy.SummaryEnabled = true
	policy.SummaryItemTokenThreshold = 300
	if err := pm.SetPolicy(ctx, policy); err != nil {
		t.Fatalf("SetPolicy failed: %v", err)
	}
	resolved, _ = pm.GetPolicy(ctx, namespace)
	if !resolved.SummaryEnabled || resolved.SummaryItemTokenThreshold != 300 {
		t.Fatalf("expected the updated policy, got %+v", resolved)
	}

	// A policy built only from a namespace does not lose the rank weights.
	bare := model.NamespacePolicy{Namespace: namespace, SummaryEnabled: false}
	if err := pm.SetPolicy(ctx, bare); err != nil {
		t.Fatalf("SetPolicy failed: %v", err)
	}
	resolved, _ = pm.GetPolicy(ctx, namespace)
	fts, recency, importance, confidence := pm.GetRankWeights(resolved)
	if fts+recency+importance+confidence <= 0 {
		t.Fatalf("expected usable rank weights, got %v/%v/%v/%v", fts, recency, importance, confidence)
	}
}

func TestRemember_DefaultConfidenceIsRecallable(t *testing.T) {
	svc, db := newSvc(t)
	ctx := context.Background()
	scope := testScope()

	id, err := svc.Remember(ctx, scope, RememberRequest{NamespaceType: model.NamespaceTypeKnowledge, Content: "unscored memory"})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	var item model.MemoryItem
	if err := db.First(&item, "id = ?", id).Error; err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if item.Confidence != DefaultConfidence {
		t.Fatalf("expected the default confidence %v, got %v", DefaultConfidence, item.Confidence)
	}

	hits, err := svc.Recall(ctx, scope, RecallRequest{Query: "unscored", TopK: 5})
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected the unscored memory to be recallable at the default floor, got %d", len(hits))
	}
}
