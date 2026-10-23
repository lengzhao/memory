package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
	"github.com/lengzhao/memory/store"
)

// fakeLLM is an offline stand-in for an OpenAI-compatible endpoint. It is what makes the extraction
// and decision paths testable without a network, a key or a cost.
type fakeLLM struct {
	t        *testing.T
	server   *httptest.Server
	requests []openAIRequest
	replies  []string // consumed in order; the last one repeats
	status   int
	failWith string
	onCall   func(req openAIRequest) string
}

func newFakeLLM(t *testing.T, replies ...string) *fakeLLM {
	t.Helper()
	fake := &fakeLLM{t: t, replies: replies, status: http.StatusOK}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req openAIRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fake.requests = append(fake.requests, req)

		if fake.failWith != "" {
			w.WriteHeader(fake.status)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"message": fake.failWith, "type": "invalid_request_error"},
			})
			return
		}

		reply := ""
		switch {
		case fake.onCall != nil:
			reply = fake.onCall(req)
		case len(fake.replies) > 1:
			reply = fake.replies[0]
			fake.replies = fake.replies[1:]
		case len(fake.replies) == 1:
			reply = fake.replies[0]
		default:
			reply = `{"memories":[]}`
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   req.Model,
			"choices": []map[string]interface{}{
				{"index": 0, "message": map[string]string{"role": "assistant", "content": reply}, "finish_reason": "stop"},
			},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
		})
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// config returns an LLMConfig pointed at the fake endpoint.
func (f *fakeLLM) config() *model.LLMConfig {
	baseURL := f.server.URL
	return &model.LLMConfig{
		APIKey:         "test-key",
		BaseURL:        &baseURL,
		Model:          "test-model",
		MaxTokens:      1024,
		TimeoutSeconds: 10,
	}
}

// lastPrompt returns the concatenated messages of the most recent request.
func (f *fakeLLM) lastPrompt() string {
	if len(f.requests) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, msg := range f.requests[len(f.requests)-1].Messages {
		builder.WriteString(msg.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

func (f *fakeLLM) callCount() int { return len(f.requests) }

// ---- extraction through the fake -----------------------------------------------------------

func TestExtract_OfflineThroughFakeLLM(t *testing.T) {
	db := testDB(t)
	extractor := NewExtractor(db)
	fake := newFakeLLM(t, `{"memories":[
		{"namespace":"profile","title":"主题偏好","content":"用户偏好深色主题","summary":"偏好深色主题","tags":["ui"],"importance":60,"confidence":0.92},
		{"namespace":"action","title":"季度复盘","content":"下周三前提交季度复盘","tags":["review"],"importance":80,"confidence":0.88}
	]}`)

	result, err := extractor.Extract(context.Background(), testScope(), ExtractRequest{
		DialogText: "我喜欢深色主题，另外下周三要交季度复盘",
		LLMConfig:  fake.config(),
	})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if len(result.Memories) != 2 {
		t.Fatalf("expected 2 extracted memories, got %d", len(result.Memories))
	}
	if fake.callCount() != 1 {
		t.Fatalf("expected exactly one model call, got %d", fake.callCount())
	}

	// Both memories are persisted through the normal write path.
	var count int64
	db.Model(&model.MemoryItem{}).Count(&count)
	if count != 2 {
		t.Fatalf("expected 2 persisted memories, got %d", count)
	}
}

func TestExtract_DryRunDoesNotPersistButCallsModel(t *testing.T) {
	db := testDB(t)
	extractor := NewExtractor(db)
	fake := newFakeLLM(t, `{"memories":[{"namespace":"profile","content":"用户偏好中文","confidence":0.9}]}`)

	result, err := extractor.Extract(context.Background(), testScope(), ExtractRequest{
		DialogText: "用中文回复我",
		LLMConfig:  fake.config(),
		DryRun:     true,
	})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if len(result.Memories) != 1 {
		t.Fatalf("expected the preview to list the memory, got %d", len(result.Memories))
	}

	var count int64
	db.Model(&model.MemoryItem{}).Count(&count)
	if count != 0 {
		t.Fatalf("a dry run must not persist, got %d rows", count)
	}
	if fake.callCount() != 1 {
		t.Fatalf("a dry run must still call the model, got %d calls", fake.callCount())
	}
}

func TestExtract_APIErrorSurfacesAndRecordsFailure(t *testing.T) {
	db := testDB(t)
	extractor := NewExtractor(db)
	fake := newFakeLLM(t)
	fake.status = http.StatusUnauthorized
	fake.failWith = "Incorrect API key provided"

	_, err := extractor.Extract(context.Background(), testScope(), ExtractRequest{
		DialogText: "anything",
		LLMConfig:  fake.config(),
	})
	if err == nil {
		t.Fatal("expected the API error to surface")
	}
	if !strings.Contains(err.Error(), "Incorrect API key") {
		t.Fatalf("expected the provider message, got %v", err)
	}

	var record model.DialogExtraction
	if err := db.Order("created_at DESC").First(&record).Error; err != nil {
		t.Fatalf("expected an extraction record, got %v", err)
	}
	if record.Status != model.ExtractionStatusFailed || record.ErrorMessage == nil {
		t.Fatalf("expected a failed record with its message, got %+v", record)
	}
}

// ---- session summary rolling ----------------------------------------------------------------

func TestExtract_RollsSessionSummaryInTheSameCall(t *testing.T) {
	db := testDB(t)
	extractor := NewExtractor(db)
	scope := testScope()
	ctx := context.Background()

	fake := newFakeLLM(t,
		`{"session_summary":"用户在准备季度复盘，已确认下周三交付。","memories":[{"namespace":"action","title":"季度复盘","content":"下周三前提交季度复盘","confidence":0.9}]}`,
		`{"session_summary":"用户在准备季度复盘并新增了数据来源要求；下周三交付不变，需补充数据口径。","memories":[{"namespace":"action","title":"数据来源","content":"复盘需补充数据口径说明","confidence":0.9}]}`,
	)

	first, err := extractor.Extract(ctx, scope, ExtractRequest{
		DialogText:         "下周三我要交季度复盘",
		LLMConfig:          fake.config(),
		RollSessionSummary: true,
	})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if first.SessionSummary == nil {
		t.Fatal("expected a session summary on the first extraction")
	}
	if first.SessionSummary.Revision != 1 || first.SessionSummary.Source != model.SessionSummarySourceLLM {
		t.Fatalf("unexpected first digest: %+v", first.SessionSummary)
	}

	second, err := extractor.Extract(ctx, scope, ExtractRequest{
		DialogText:         "还要补一下数据口径",
		LLMConfig:          fake.config(),
		RollSessionSummary: true,
	})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if second.SessionSummary == nil || second.SessionSummary.Revision != 2 {
		t.Fatalf("expected the digest to roll to revision 2, got %+v", second.SessionSummary)
	}

	// The second call must have been handed the previous digest to integrate.
	if prompt := fake.lastPrompt(); !strings.Contains(prompt, "用户在准备季度复盘，已确认下周三交付") {
		t.Fatal("expected the previous digest to be passed back to the model")
	}
	if prompt := fake.lastPrompt(); !strings.Contains(prompt, "SESSION SUMMARY REQUEST") {
		t.Fatal("expected the digest instruction in the prompt")
	}

	// Two extractions, two calls: rolling the digest adds no request.
	if fake.callCount() != 2 {
		t.Fatalf("expected one call per extraction, got %d", fake.callCount())
	}

	stored, err := NewMemoryService(db).GetSessionSummary(ctx, scope)
	if err != nil || stored == nil {
		t.Fatalf("expected a stored digest, got %+v (%v)", stored, err)
	}
	if !strings.Contains(stored.Summary, "数据口径") || stored.Revision != 2 {
		t.Fatalf("unexpected stored digest: %+v", stored)
	}
}

func TestExtract_RolledSummarySurvivesPersistFailure(t *testing.T) {
	db := testDB(t)
	extractor := NewExtractor(db)
	scope := testScope()

	// The model returns a digest plus an invalid memory (empty content), which the write path
	// rejects. The digest must still be stored.
	fake := newFakeLLM(t, `{"session_summary":"用户正在排查构建失败。","memories":[{"namespace":"profile","content":"","confidence":0.9}]}`)

	result, err := extractor.Extract(context.Background(), scope, ExtractRequest{
		DialogText:         "构建又失败了",
		LLMConfig:          fake.config(),
		RollSessionSummary: true,
	})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if result.SessionSummary == nil {
		t.Fatal("expected the digest to be stored despite the persist failure")
	}

	var count int64
	db.Model(&model.MemoryItem{}).Count(&count)
	if count != 0 {
		t.Fatalf("expected the invalid memory to be rejected, got %d rows", count)
	}
}

func TestExtract_DryRunStillReportsRolledSummary(t *testing.T) {
	db := testDB(t)
	extractor := NewExtractor(db)
	fake := newFakeLLM(t, `{"session_summary":"用户在评估两个方案。","memories":[]}`)

	result, err := extractor.Extract(context.Background(), testScope(), ExtractRequest{
		DialogText:         "哪个方案更好",
		LLMConfig:          fake.config(),
		DryRun:             true,
		RollSessionSummary: true,
	})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if result.SessionSummary == nil || result.SessionSummary.Summary == "" {
		t.Fatalf("expected the digest to be reported on a dry run, got %+v", result.SessionSummary)
	}
}

func TestExtract_NoSessionSummaryRequestedKeepsBehaviour(t *testing.T) {
	db := testDB(t)
	extractor := NewExtractor(db)
	fake := newFakeLLM(t, `{"session_summary":"should be ignored","memories":[{"namespace":"profile","content":"x","confidence":0.9}]}`)

	result, err := extractor.Extract(context.Background(), testScope(), ExtractRequest{
		DialogText: "hi",
		LLMConfig:  fake.config(),
	})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if result.SessionSummary != nil {
		t.Fatalf("expected no digest when not requested, got %+v", result.SessionSummary)
	}
	var count int64
	db.Model(&model.SessionSummary{}).Count(&count)
	if count != 0 {
		t.Fatalf("expected no stored digest, got %d", count)
	}
	if prompt := fake.lastPrompt(); strings.Contains(prompt, "SESSION SUMMARY REQUEST") {
		t.Fatal("expected the digest instruction to be omitted")
	}
}

// ---- session summary storage ----------------------------------------------------------------

func TestSessionSummary_HostWrittenAndScoped(t *testing.T) {
	db := testDB(t)
	svc := NewMemoryService(db)
	scope := testScope()
	otherScope := otherTenantScope()
	ctx := context.Background()

	if _, err := svc.SetSessionSummary(ctx, scope, "用户在调试内存库的检索排序。"); err != nil {
		t.Fatalf("SetSessionSummary failed: %v", err)
	}

	stored, err := svc.GetSessionSummary(ctx, scope)
	if err != nil || stored == nil {
		t.Fatalf("expected a stored digest, got %+v (%v)", stored, err)
	}
	if stored.Source != model.SessionSummarySourceHost || stored.Revision != 1 {
		t.Fatalf("unexpected digest metadata: %+v", stored)
	}
	if stored.TokenCount == 0 {
		t.Fatal("expected a token count on the digest")
	}

	// Another tenant's session is a different session with a different digest.
	if other, err := svc.GetSessionSummary(ctx, otherScope); err != nil || other != nil {
		t.Fatalf("expected no digest for another tenant, got %+v (%v)", other, err)
	}

	// A second session of the same user has its own digest.
	other := otherSessionScope()
	if _, err := svc.SetSessionSummary(ctx, other, "另一个会话。"); err != nil {
		t.Fatalf("SetSessionSummary failed: %v", err)
	}
	first, _ := svc.GetSessionSummary(ctx, scope)
	if first.Summary != "用户在调试内存库的检索排序。" {
		t.Fatalf("a sibling session clobbered this digest: %+v", first)
	}

	// Updating replaces the digest and bumps the revision.
	if _, err := svc.SetSessionSummary(ctx, scope, "用户在调试检索排序，已定位到 BM25 符号问题。"); err != nil {
		t.Fatalf("SetSessionSummary failed: %v", err)
	}
	updated, _ := svc.GetSessionSummary(ctx, scope)
	if updated.Revision != 2 || !strings.Contains(updated.Summary, "BM25") {
		t.Fatalf("expected an updated revision, got %+v", updated)
	}

	// Clearing is explicit and reports when there was nothing to clear.
	if err := svc.ClearSessionSummary(ctx, scope); err != nil {
		t.Fatalf("ClearSessionSummary failed: %v", err)
	}
	if got, _ := svc.GetSessionSummary(ctx, scope); got != nil {
		t.Fatalf("expected the digest to be gone, got %+v", got)
	}
	var memErr *MemoryError
	if err := svc.ClearSessionSummary(ctx, scope); !errors.As(err, &memErr) || memErr.Code != CodeNotFound {
		t.Fatalf("expected NOT_FOUND on a second clear, got %v", err)
	}

	// Validation and the global scope have no session to attach a digest to.
	if _, err := svc.SetSessionSummary(ctx, scope, "   "); err == nil {
		t.Fatal("expected an empty digest to be rejected")
	}
	if _, err := svc.SetSessionSummary(ctx, GlobalScope(), "x"); err == nil {
		t.Fatal("expected the global scope to be rejected")
	}
}

func TestSessionSummary_RollupFallback(t *testing.T) {
	db := testDB(t)
	svc := NewMemoryService(db)
	scope := testScope()
	ctx := context.Background()

	// No digest and no memories: nothing to say yet.
	if ensured, err := svc.EnsureSessionSummary(ctx, scope, 200); err != nil || ensured != nil {
		t.Fatalf("expected no digest for an empty session, got %+v (%v)", ensured, err)
	}

	for i := 0; i < 3; i++ {
		if _, err := svc.Remember(ctx, scope, RememberRequest{
			NamespaceType: model.NamespaceTypeTransient,
			Title:         "话题" + itoa(i),
			Content:       "会话内容 " + itoa(i),
			Tags:          []string{"debug"},
			Confidence:    0.9,
		}); err != nil {
			t.Fatalf("remember failed: %v", err)
		}
	}

	rollup, err := svc.EnsureSessionSummary(ctx, scope, 200)
	if err != nil {
		t.Fatalf("EnsureSessionSummary failed: %v", err)
	}
	if rollup == nil {
		t.Fatal("expected a deterministic rollup")
	}
	if rollup.Source != model.SessionSummarySourceRollup {
		t.Fatalf("expected the rollup source, got %q", rollup.Source)
	}
	if !strings.Contains(rollup.Summary, "话题0") {
		t.Fatalf("expected the session's topics in the rollup, got %q", rollup.Summary)
	}
	if CountTokens(rollup.Summary) > 200 {
		t.Fatalf("rollup exceeds its budget: %d tokens", CountTokens(rollup.Summary))
	}

	// Once a model- or host-written digest exists, the rollup does not overwrite it.
	if _, err := svc.SetSessionSummary(ctx, scope, "用户在调试检索排序。"); err != nil {
		t.Fatalf("SetSessionSummary failed: %v", err)
	}
	kept, err := svc.EnsureSessionSummary(ctx, scope, 200)
	if err != nil {
		t.Fatalf("EnsureSessionSummary failed: %v", err)
	}
	if kept.Source != model.SessionSummarySourceHost || kept.Summary != "用户在调试检索排序。" {
		t.Fatalf("the rollup overwrote a better digest: %+v", kept)
	}
}

func TestRecallContext_IncludesSessionSummaryFirst(t *testing.T) {
	db := testDB(t)
	svc := NewMemoryService(db)
	scope := testScope()
	ctx := context.Background()

	if _, err := svc.SetSessionSummary(ctx, scope, "用户在修复检索排序，已确认 BM25 符号问题。"); err != nil {
		t.Fatalf("SetSessionSummary failed: %v", err)
	}
	if _, err := svc.Remember(ctx, scope, RememberRequest{
		NamespaceType: model.NamespaceTypeAction, Title: "修复排序", Content: "修正 bm25 映射并加回归测试", Confidence: 0.9,
	}); err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	bundle, err := svc.RecallContext(ctx, scope, RecallRequest{NamespaceTypes: []model.NamespaceType{model.NamespaceTypeAction}}, RecallBudget{
		IncludeSessionSummary: true,
		MaxTokens:             400,
	})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if bundle.SessionSummary == nil {
		t.Fatal("expected the session digest in the bundle")
	}
	if !strings.HasPrefix(bundle.Text, sessionSummaryHeading) {
		t.Fatalf("expected the digest first in the assembled text, got %q", bundle.Text)
	}
	if !strings.Contains(bundle.Text, "BM25") {
		t.Fatalf("expected the digest content, got %q", bundle.Text)
	}
	if bundle.Tokens != CountTokens(bundle.Text) {
		t.Fatalf("reported tokens (%d) disagree with the text (%d)", bundle.Tokens, CountTokens(bundle.Text))
	}
	if bundle.Tokens > 400 {
		t.Fatalf("bundle exceeds the budget: %d", bundle.Tokens)
	}

	// The digest respects its own budget and is trimmed, not dropped.
	tight, err := svc.RecallContext(ctx, scope, RecallRequest{NamespaceTypes: []model.NamespaceType{model.NamespaceTypeAction}}, RecallBudget{
		IncludeSessionSummary:   true,
		SessionSummaryMaxTokens: 5,
		NoSessionRollup:         true,
	})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if tight.SessionSummary == nil || CountTokens(tight.SessionSummary.Summary) > 5 {
		t.Fatalf("expected a trimmed digest, got %+v", tight.SessionSummary)
	}

	// Not requested: the digest stays out of the context.
	plain, err := svc.RecallContext(ctx, scope, RecallRequest{NamespaceTypes: []model.NamespaceType{model.NamespaceTypeAction}}, RecallBudget{})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if plain.SessionSummary != nil || strings.Contains(plain.Text, sessionSummaryHeading) {
		t.Fatalf("expected no digest when not requested, got %+v", plain.SessionSummary)
	}

	// The global scope has no conversation: it must not pick up anybody's digest.
	global, err := svc.RecallContext(ctx, GlobalScope(), RecallRequest{}, RecallBudget{IncludeSessionSummary: true})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if global.SessionSummary != nil {
		t.Fatalf("expected no digest for the global scope, got %+v", global.SessionSummary)
	}
}

func TestRecallContext_SessionRollupRespectsNoRollupFlag(t *testing.T) {
	db := testDB(t)
	svc := NewMemoryService(db)
	scope := testScope()
	ctx := context.Background()

	if _, err := svc.Remember(ctx, scope, RememberRequest{
		NamespaceType: model.NamespaceTypeTransient, Title: "调试检索", Content: "会话内容", Confidence: 0.9,
	}); err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	// With rolling allowed, the bundle carries a deterministic digest even before a model wrote one.
	rolled, err := svc.RecallContext(ctx, scope, RecallRequest{}, RecallBudget{IncludeSessionSummary: true})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if rolled.SessionSummary == nil || rolled.SessionSummary.Source != model.SessionSummarySourceRollup {
		t.Fatalf("expected a rollup digest, got %+v", rolled.SessionSummary)
	}

	// With the flag set, a session without a digest stays without one.
	fresh := otherSessionScope()
	if _, err := svc.Remember(ctx, fresh, RememberRequest{
		NamespaceType: model.NamespaceTypeTransient, Title: "另一个会话", Content: "内容", Confidence: 0.9,
	}); err != nil {
		t.Fatalf("remember failed: %v", err)
	}
	noRoll, err := svc.RecallContext(ctx, fresh, RecallRequest{}, RecallBudget{IncludeSessionSummary: true, NoSessionRollup: true})
	if err != nil {
		t.Fatalf("RecallContext failed: %v", err)
	}
	if noRoll.SessionSummary != nil {
		t.Fatalf("expected no digest with NoSessionRollup, got %+v", noRoll.SessionSummary)
	}
}

// ---- decision engine through the fake --------------------------------------------------------

func TestDecision_OfflineThroughFakeLLM(t *testing.T) {
	db := testDB(t)
	svc := NewMemoryService(db)
	de := NewDecisionEngine(db)
	scope := testScope()
	ctx := context.Background()

	id, err := svc.Remember(ctx, scope, RememberRequest{
		NamespaceType: model.NamespaceTypeProfile, Content: "用户喜欢喝茶", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("remember failed: %v", err)
	}

	fake := newFakeLLM(t, `{"decisions":[{"decision":"UPDATE","target_id":"`+id+`","reason":"更具体","confidence":0.9}]}`)

	similar, err := de.FindSimilarMemories(ctx, scope, ExtractedMemory{
		Namespace: model.NamespaceTypeProfile, Content: "用户喜欢喝茶", Tags: []string{"drink"},
	}, 5)
	if err != nil {
		t.Fatalf("FindSimilarMemories failed: %v", err)
	}
	if len(similar) != 1 || similar[0].ID != id {
		t.Fatalf("expected the existing memory, got %+v", similar)
	}

	result, err := de.Decide(ctx, *fake.config(), DecisionRequest{
		Candidates:      []ExtractedMemory{{TempID: "c1", Namespace: model.NamespaceTypeProfile, Content: "用户喜欢乌龙茶", Confidence: 0.9}},
		SimilarMemories: similar,
		DialogContext:   "用户说他现在只喝乌龙茶",
	})
	if err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	if len(result.Decisions) != 1 || result.Decisions[0].Decision != DecisionUpdate {
		t.Fatalf("unexpected decisions: %+v", result.Decisions)
	}
	if result.TotalTokens != 30 {
		t.Fatalf("expected the reported token usage, got %d", result.TotalTokens)
	}

	execution, err := de.ExecuteDecisions(ctx, scope,
		[]ExtractedMemory{{TempID: "c1", Namespace: model.NamespaceTypeProfile, Content: "用户喜欢乌龙茶", Confidence: 0.9}},
		result.Decisions)
	if err != nil {
		t.Fatalf("ExecuteDecisions failed: %v", err)
	}
	if len(execution.Updated) != 1 {
		t.Fatalf("expected the decision to be applied, got %+v", execution)
	}

	var item model.MemoryItem
	db.First(&item, "id = ?", id)
	if !strings.Contains(item.Content, "乌龙茶") || item.Version != 2 {
		t.Fatalf("expected the memory to be updated and versioned, got %+v", item)
	}
}

// testDB is the shared harness for the offline tests.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	return store.SetupTestDB(t)
}
