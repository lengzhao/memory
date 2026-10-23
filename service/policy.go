// Package service provides namespace policy management.
package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lengzhao/memory/model"
)

// defaultNamespacePolicies provides default policies for each namespace type.
var defaultNamespacePolicies = map[model.NamespaceType]model.NamespacePolicy{
	model.NamespaceTypeTransient: {
		Namespace:                 "transient/*",
		TTLSeconds:                intPtr(259200), // 3 days
		TTLPolicy:                 model.TTLPolicySliding,
		SlidingTTLThreshold:       3,
		SummaryEnabled:            true,
		SummaryItemTokenThreshold: 500,
		RankWeightsJSON:           `{"fts":0.55,"recency":0.20,"importance":0.15,"confidence":0.10}`,
		DefaultTopK:               10,
	},
	model.NamespaceTypeProfile: {
		Namespace:                 "profile/*",
		TTLSeconds:                nil, // No expiry
		TTLPolicy:                 model.TTLPolicyManual,
		SlidingTTLThreshold:       0,
		SummaryEnabled:            true,
		SummaryItemTokenThreshold: 500,
		RankWeightsJSON:           `{"fts":0.55,"recency":0.15,"importance":0.20,"confidence":0.10}`,
		DefaultTopK:               10,
	},
	model.NamespaceTypeAction: {
		Namespace:                 "action/*",
		TTLSeconds:                intPtr(7776000), // 90 days
		TTLPolicy:                 model.TTLPolicyFixed,
		SlidingTTLThreshold:       0,
		SummaryEnabled:            true,
		SummaryItemTokenThreshold: 500,
		RankWeightsJSON:           `{"fts":0.50,"recency":0.25,"importance":0.15,"confidence":0.10}`,
		DefaultTopK:               10,
	},
	model.NamespaceTypeKnowledge: {
		Namespace:                 "knowledge/*",
		TTLSeconds:                nil, // No expiry
		TTLPolicy:                 model.TTLPolicyManual,
		SlidingTTLThreshold:       0,
		SummaryEnabled:            true,
		SummaryItemTokenThreshold: 1000,
		RankWeightsJSON:           `{"fts":0.60,"recency":0.10,"importance":0.20,"confidence":0.10}`,
		DefaultTopK:               10,
	},
}

// PolicyManager handles namespace policy resolution.
type PolicyManager struct {
	db *gorm.DB
}

// NewPolicyManager creates a new policy manager.
func NewPolicyManager(db *gorm.DB) *PolicyManager {
	return &PolicyManager{db: db}
}

// GetPolicy retrieves the effective policy for a namespace.
//
// Resolution order:
//  1. exact namespace match,
//  2. the longest matching prefix rule (e.g. "knowledge/projA/*" for "knowledge/projA/123"),
//  3. the default policy of the namespace's type.
//
// Step 2 is what makes wildcard policies work: previously only exact matches were honoured,
// so every wildcard policy was silently ignored.
func (pm *PolicyManager) GetPolicy(ctx context.Context, namespace string) (model.NamespacePolicy, error) {
	var policies []model.NamespacePolicy
	if err := pm.db.WithContext(ctx).Find(&policies).Error; err != nil {
		return model.NamespacePolicy{}, wrapErr(CodeInternal, "query policy failed", err)
	}

	if policy, ok := matchPolicy(policies, namespace); ok {
		return policy, nil
	}

	// Fall back to the type default
	if defaultPolicy, ok := defaultNamespacePolicies[inferNamespaceType(namespace)]; ok {
		return defaultPolicy, nil
	}

	// Ultimate fallback
	return defaultNamespacePolicies[model.NamespaceTypeTransient], nil
}

// matchPolicy returns the most specific policy matching namespace: an exact match wins,
// otherwise the longest non-wildcard prefix wins.
func matchPolicy(policies []model.NamespacePolicy, namespace string) (model.NamespacePolicy, bool) {
	var best model.NamespacePolicy
	bestLen := -1
	found := false

	for _, p := range policies {
		rule := strings.TrimSpace(p.Namespace)
		if rule == "" {
			continue
		}
		switch {
		case rule == namespace:
			return p, true
		case strings.HasSuffix(rule, "/*"):
			prefix := strings.TrimSuffix(rule, "*")
			if strings.HasPrefix(namespace, prefix) && len(prefix) > bestLen {
				best, bestLen, found = p, len(prefix), true
			}
		}
	}
	return best, found
}

// DefaultNamespacePolicy returns the baseline policy of a namespace type, so callers can override
// individual fields without restating the rest:
//
//	policy := memory.DefaultNamespacePolicy(memory.NamespaceKnowledge)
//	policy.SummaryEnabled = false
//	pm.SetPolicy(ctx, policy)
func DefaultNamespacePolicy(nsType model.NamespaceType) model.NamespacePolicy {
	policy, ok := defaultNamespacePolicies[nsType]
	if !ok {
		policy = defaultNamespacePolicies[model.NamespaceTypeTransient]
	}
	policy.CreatedAt, policy.UpdatedAt = time.Time{}, time.Time{}
	return policy
}

// Policy returns the baseline policy of a namespace type, already bound to the namespace that the
// scope would write that type into. It is the scoped counterpart of DefaultNamespacePolicy:
//
//	policy := scope.Policy(memory.NamespaceKnowledge)
//	policy.SummaryEnabled = false
//	policyManager.SetPolicy(ctx, policy)
func (s Scope) Policy(nsType model.NamespaceType) model.NamespacePolicy {
	policy := DefaultNamespacePolicy(nsType)
	policy.Namespace = s.Namespace(nsType)
	return policy
}

// SetPolicy writes a namespace policy. The namespace may be an exact name or a wildcard rule like
// "knowledge/projA/*"; the most specific matching rule wins at read time.
//
// The policy replaces the stored one: every field is written verbatim, including SummaryEnabled=false
// and a zero token threshold. Start from DefaultNamespacePolicy to get sensible baselines.
func (pm *PolicyManager) SetPolicy(ctx context.Context, policy model.NamespacePolicy) error {
	if strings.TrimSpace(policy.Namespace) == "" {
		return newErr(CodeValidation, "namespace must not be empty")
	}
	// Two fields cannot legitimately be empty: an empty weight document would silently fall back to
	// the built-in weights, and an empty TTL policy has no meaning. Fill them from the type default.
	if strings.TrimSpace(policy.RankWeightsJSON) == "" {
		policy.RankWeightsJSON = defaultNamespacePolicies[model.NamespaceTypeTransient].RankWeightsJSON
		if base, ok := defaultNamespacePolicies[inferNamespaceType(policy.Namespace)]; ok {
			policy.RankWeightsJSON = base.RankWeightsJSON
		}
	}
	if policy.TTLPolicy == "" {
		policy.TTLPolicy = model.TTLPolicyManual
	}
	if policy.DefaultTopK <= 0 {
		policy.DefaultTopK = defaultTopK
	}

	now := time.Now()
	policy.CreatedAt = now
	policy.UpdatedAt = now

	var existing model.NamespacePolicy
	err := pm.db.WithContext(ctx).Where("namespace = ?", policy.Namespace).First(&existing).Error
	switch {
	case err == gorm.ErrRecordNotFound:
		if err := pm.db.WithContext(ctx).Create(&policy).Error; err != nil {
			return wrapErr(CodeInternal, "create policy failed", err)
		}
		return nil
	case err != nil:
		return wrapErr(CodeInternal, "query policy failed", err)
	}

	// Update existing: an explicit column map keeps false/0/nil meaningful instead of being skipped
	// as "unset".
	updates := map[string]interface{}{
		"ttl_seconds":                  policy.TTLSeconds,
		"ttl_policy":                   policy.TTLPolicy,
		"sliding_ttl_threshold":        policy.SlidingTTLThreshold,
		"summary_enabled":              policy.SummaryEnabled,
		"summary_item_token_threshold": policy.SummaryItemTokenThreshold,
		"rank_weights_json":            policy.RankWeightsJSON,
		"default_top_k":                policy.DefaultTopK,
		"updated_at":                   now,
	}
	if err := pm.db.WithContext(ctx).Model(&model.NamespacePolicy{}).
		Where("namespace = ?", policy.Namespace).
		Updates(updates).Error; err != nil {
		return wrapErr(CodeInternal, "update policy failed", err)
	}
	return nil
}

// GetRankWeights parses the rank weights JSON from a policy.
func (pm *PolicyManager) GetRankWeights(policy model.NamespacePolicy) (fts, recency, importance, confidence float64) {
	if strings.TrimSpace(policy.RankWeightsJSON) == "" {
		return defaultRankFTS, defaultRankRecency, defaultRankImportance, defaultRankConfidence
	}
	var weights struct {
		FTS        float64 `json:"fts"`
		Recency    float64 `json:"recency"`
		Importance float64 `json:"importance"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(policy.RankWeightsJSON), &weights); err != nil {
		return defaultRankFTS, defaultRankRecency, defaultRankImportance, defaultRankConfidence
	}
	return weights.FTS, weights.Recency, weights.Importance, weights.Confidence
}

// Built-in fallback rank weights.
const (
	defaultRankFTS        = 0.55
	defaultRankRecency    = 0.20
	defaultRankImportance = 0.15
	defaultRankConfidence = 0.10
)

// defaultTopK is the recall fan-out used when a policy does not specify one.
const defaultTopK = 10

// inferNamespaceType extracts the namespace type from a namespace string.
//
// Both layouts are understood:
//
//	profile/default, knowledge/topic              (global)
//	tenant/t/user/u/profile, tenant/t/user/u/session/s/agent/a/transient   (isolated)
//
// The isolated layout matters because it is the recommended multi-tenant one: without it every
// isolated namespace resolved to the transient policy and silently got the wrong TTL/rank weights.
func inferNamespaceType(namespace string) model.NamespaceType {
	ns := strings.TrimSpace(namespace)
	if strings.HasPrefix(ns, "tenant/") {
		if idx := strings.LastIndex(ns, "/"); idx >= 0 && idx < len(ns)-1 {
			ns = ns[idx+1:]
		}
	}
	switch {
	case strings.HasPrefix(ns, string(model.NamespaceTypeTransient)):
		return model.NamespaceTypeTransient
	case strings.HasPrefix(ns, string(model.NamespaceTypeProfile)):
		return model.NamespaceTypeProfile
	case strings.HasPrefix(ns, string(model.NamespaceTypeAction)):
		return model.NamespaceTypeAction
	case strings.HasPrefix(ns, string(model.NamespaceTypeKnowledge)):
		return model.NamespaceTypeKnowledge
	default:
		return model.NamespaceTypeTransient
	}
}

func intPtr(i int) *int {
	return &i
}
