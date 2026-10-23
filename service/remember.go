package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// Config holds configuration and callbacks for MemoryService.
type Config struct {
	// Lifecycle callbacks (all optional).
	OnCreated func(ctx context.Context, item model.MemoryItem)
	OnUpdated func(ctx context.Context, item model.MemoryItem)
	OnDeleted func(ctx context.Context, itemID string)
	OnExpired func(ctx context.Context, itemID string)

	// ApplyPolicies derives expires_at from the namespace TTL policy when a request carries no
	// explicit TTL. A namespace whose policy has no TTL (profile/knowledge by default) stays
	// permanent either way.
	ApplyPolicies bool

	// RecordEvents enables the memory_events audit trail for writes. Actor/trace identifiers
	// come from WithActor/WithTrace.
	RecordEvents bool
}

// MemoryService is the single write path for memories. Every mutation (including those driven by
// the extraction and decision engines) goes through it, so indexing, versioning, callbacks and
// auditing cannot be bypassed.
type MemoryService struct {
	db             *gorm.DB
	config         Config
	accessStatsSem chan struct{}
	policyManager  *PolicyManager
}

// NewMemoryService creates a service. Use WithConfig to add callbacks, policy application and
// the audit trail.
func NewMemoryService(db *gorm.DB) *MemoryService {
	return &MemoryService{
		db:             db,
		accessStatsSem: make(chan struct{}, 8),
		policyManager:  NewPolicyManager(db),
	}
}

// WithConfig applies configuration. It must be called before the service is shared between
// goroutines.
func (s *MemoryService) WithConfig(config Config) *MemoryService {
	s.config = config
	return s
}

// MaxContentRunes bounds the size of a single memory row.
const MaxContentRunes = 100_000

// DefaultConfidence is stored for a memory whose request did not state a confidence. It is high
// because an unscored memory should not be filtered out by DefaultMinConfidence, while a host that
// wants a memory to rank lower states a lower value.
const DefaultConfidence = 1.0

// RememberRequest stores one memory.
//
// The scope is an argument, not a context value: an operation without a scope is a compile error.
// Content is always stored in exactly one namespace, derived from the scope and the namespace type.
type RememberRequest struct {
	NamespaceType model.NamespaceType
	Title         string
	Content       string
	Summary       string
	Tags          []string
	SourceType    model.SourceType
	SourceRef     string
	Importance    int
	Confidence    float64
	TTLSeconds    *int
	// DedupeKey makes the write idempotent. It is scoped automatically (see Scope.DedupeKey).
	DedupeKey *string
	// Ephemeral marks a transient reminder that should not outlive its session by much; the
	// namespace policy decides whether it gets a TTL.
	Ephemeral bool
}

// Remember stores a memory and returns its ID. Re-remembering the same DedupeKey returns the
// existing ID instead of creating a second row.
func (s *MemoryService) Remember(ctx context.Context, scope Scope, req RememberRequest) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	nsType, err := normalizeNamespaceType(req.NamespaceType)
	if err != nil {
		return "", err
	}
	req.NamespaceType = nsType
	if err := req.validate(); err != nil {
		return "", err
	}

	namespace := scope.Namespace(nsType)
	if req.DedupeKey != nil {
		scoped := scope.DedupeKey(nsType, *req.DedupeKey)
		if scoped == "" {
			req.DedupeKey = nil
		} else {
			req.DedupeKey = &scoped
		}
	}

	now := time.Now()

	if req.DedupeKey != nil {
		existing, err := s.findByDedupeKey(ctx, namespace, *req.DedupeKey)
		if err != nil {
			return "", err
		}
		if existing != "" {
			return existing, nil
		}
	}

	expiresAt, err := s.resolveExpiresAt(ctx, scope, req.TTLSeconds, nsType)
	if err != nil {
		return "", err
	}

	// An unset confidence means "not scored by the host", which is the honest reading of a memory
	// that must still be recallable. Normalising it here keeps the stored value meaningful instead
	// of depending on a column default.
	confidence := req.Confidence
	if confidence == 0 {
		confidence = DefaultConfidence
	}

	tagsJSON, _ := json.Marshal(req.Tags)
	// Always build a fresh row: GORM reuses the primary key of a populated model on Create, so a
	// recycled struct silently overwrites the row it was loaded from.
	item := model.MemoryItem{
		ID:            model.GenerateID(),
		Namespace:     namespace,
		NamespaceType: req.NamespaceType,
		Title:         req.Title,
		Content:       req.Content,
		Summary:       req.Summary,
		TagsJSON:      string(tagsJSON),
		TokenizedText: TokenizeForSearch(req.Title + " " + req.Content + " " + req.Summary),
		SourceType:    req.SourceType,
		SourceRef:     req.SourceRef,
		Importance:    req.Importance,
		Confidence:    confidence,
		Status:        model.ItemStatusActive,
		ExpiresAt:     expiresAt,
		CreatedAt:     now,
		UpdatedAt:     now,
		Version:       1,
		DedupeKey:     req.DedupeKey,
	}

	if err := createRow(ctx, s.db, &item); err != nil {
		if req.DedupeKey != nil && isUniqueConstraintError(err) {
			// Lost the race against a concurrent writer with the same key: that is success.
			if existing, lookupErr := s.findByDedupeKey(ctx, namespace, *req.DedupeKey); lookupErr == nil && existing != "" {
				return existing, nil
			}
			return "", wrapErr(CodeInternal, "dedupe conflict but the existing row could not be loaded", err)
		}
		return "", wrapErr(CodeInternal, "failed to create memory", err)
	}

	s.recordEvent(ctx, model.EventTypeCreate, &item.ID, namespace, map[string]interface{}{
		"source_type": string(req.SourceType),
		"source_ref":  req.SourceRef,
	})
	if s.config.OnCreated != nil {
		s.config.OnCreated(ctx, item)
	}
	return item.ID, nil
}

func (r RememberRequest) validate() error {
	if strings.TrimSpace(r.Content) == "" {
		return newErr(CodeValidation, "content must not be empty")
	}
	if len([]rune(r.Content)) > MaxContentRunes {
		return newErr(CodeValidation, fmt.Sprintf("content exceeds %d runes", MaxContentRunes))
	}
	if r.Importance < 0 || r.Importance > 100 {
		return newErr(CodeValidation, fmt.Sprintf("importance must be within 0-100, got %d", r.Importance))
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		return newErr(CodeValidation, fmt.Sprintf("confidence must be within 0.0-1.0, got %v", r.Confidence))
	}
	if r.TTLSeconds != nil && *r.TTLSeconds < 0 {
		return newErr(CodeValidation, "ttl_seconds must not be negative")
	}
	return nil
}

// isUniqueConstraintError reports a duplicate-key violation (the driver wording varies).
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

// findByDedupeKey returns the ID of an existing row with the same scoped key, or "" when there is
// none. It is read-only, so callers may safely continue with an insert afterwards.
func (s *MemoryService) findByDedupeKey(ctx context.Context, namespace, key string) (string, error) {
	var existing model.MemoryItem
	err := s.db.WithContext(ctx).
		Select("id").
		Where("namespace = ? AND dedupe_key = ?", namespace, key).
		First(&existing).Error
	switch {
	case err == nil:
		return existing.ID, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "", nil
	default:
		return "", wrapErr(CodeInternal, "failed to check dedupe key", err)
	}
}

// resolveExpiresAt returns the expiry for a new row: an explicit TTL wins, otherwise the
// namespace policy applies when Config.ApplyPolicies is enabled.
func (s *MemoryService) resolveExpiresAt(ctx context.Context, scope Scope, ttlSeconds *int, nsType model.NamespaceType) (*time.Time, error) {
	if ttlSeconds != nil {
		if *ttlSeconds <= 0 {
			return nil, nil
		}
		expiry := time.Now().Add(time.Duration(*ttlSeconds) * time.Second)
		return &expiry, nil
	}
	if !s.config.ApplyPolicies {
		return nil, nil
	}
	policy, err := s.policyManager.GetPolicy(ctx, scope.Namespace(nsType))
	if err != nil {
		return nil, err
	}
	if policy.TTLSeconds == nil || *policy.TTLSeconds <= 0 {
		return nil, nil
	}
	expiry := time.Now().Add(time.Duration(*policy.TTLSeconds) * time.Second)
	return &expiry, nil
}
