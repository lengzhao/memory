// Package service provides the core memory service implementation.
package service

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lengzhao/memory/model"
)

// Scope is the isolation identity of a memory operation. It is an explicit, validated value
// rather than a context payload: a caller cannot reach the database without naming the scope
// it acts on, so "forgot to pass the identity" is a compile-time omission instead of a runtime
// cross-tenant read.
//
// Namespace layout:
//
//	profile / action / knowledge  ->  tenant/<tenant>/user/<user>/<type>   (跨会话共享)
//	transient                     ->  tenant/<tenant>/user/<user>/session/<session>/agent/<agent>/transient
//
// The zero Scope is the global scope (single-user / local deployments), reachable only by
// asking for it explicitly via GlobalScope.
type Scope struct {
	TenantID  string
	UserID    string
	SessionID string
	AgentID   string
}

// NewScope builds a validated scope. All four identifiers are required when running multi-tenant:
// leaving one out is a bug in the host, and silently substituting a default is how data leaks
// across users, so it fails here rather than at the storage layer.
func NewScope(tenantID, userID, sessionID, agentID string) (Scope, error) {
	scope := Scope{
		TenantID:  strings.TrimSpace(tenantID),
		UserID:    strings.TrimSpace(userID),
		SessionID: strings.TrimSpace(sessionID),
		AgentID:   strings.TrimSpace(agentID),
	}
	if err := scope.Validate(); err != nil {
		return Scope{}, err
	}
	return scope, nil
}

// MustScope is NewScope for scopes known to be well-formed at init time (tests, bootstrap).
func MustScope(tenantID, userID, sessionID, agentID string) Scope {
	scope, err := NewScope(tenantID, userID, sessionID, agentID)
	if err != nil {
		panic(err)
	}
	return scope
}

// GlobalScope returns the scope for single-user deployments and maintenance jobs. Data written
// under it lives in the non-prefixed namespaces ("profile/default", ...).
func GlobalScope() Scope {
	return Scope{}
}

// IsGlobal reports whether the scope addresses the non-prefixed global namespaces.
func (s Scope) IsGlobal() bool {
	return !s.any()
}

// Validate reports why a scope cannot be used for an operation.
func (s Scope) Validate() error {
	if s.IsGlobal() {
		return nil
	}
	var missing []string
	if s.TenantID == "" {
		missing = append(missing, "tenant_id")
	}
	if s.UserID == "" {
		missing = append(missing, "user_id")
	}
	if s.SessionID == "" {
		missing = append(missing, "session_id")
	}
	if s.AgentID == "" {
		missing = append(missing, "agent_id")
	}
	if len(missing) > 0 {
		return newErr(CodeValidation, "incomplete scope, missing "+strings.Join(missing, ","))
	}
	return nil
}

func (s Scope) any() bool {
	return s.TenantID != "" || s.UserID != "" || s.SessionID != "" || s.AgentID != ""
}

// String is a stable, collision-free identity of the scope. It is the cache partition key for
// LLM extraction results.
func (s Scope) String() string {
	if s.IsGlobal() {
		return "global"
	}
	return strings.Join([]string{s.TenantID, s.UserID, s.SessionID, s.AgentID}, "\x1f")
}

// Namespace maps a namespace type to its concrete namespace inside this scope.
func (s Scope) Namespace(nsType model.NamespaceType) string {
	if s.IsGlobal() {
		return defaultNamespace(nsType)
	}
	switch nsType {
	case model.NamespaceTypeProfile:
		return "tenant/" + s.TenantID + "/user/" + s.UserID + "/profile"
	case model.NamespaceTypeAction:
		return "tenant/" + s.TenantID + "/user/" + s.UserID + "/action"
	case model.NamespaceTypeKnowledge:
		return "tenant/" + s.TenantID + "/user/" + s.UserID + "/knowledge"
	default:
		return "tenant/" + s.TenantID + "/user/" + s.UserID + "/session/" + s.SessionID + "/agent/" + s.AgentID + "/transient"
	}
}

// Namespaces lists the namespaces this scope may read. No types means all four.
func (s Scope) Namespaces(types ...model.NamespaceType) []string {
	if len(types) == 0 {
		types = []model.NamespaceType{
			model.NamespaceTypeTransient,
			model.NamespaceTypeProfile,
			model.NamespaceTypeAction,
			model.NamespaceTypeKnowledge,
		}
	}
	namespaces := make([]string, 0, len(types))
	for _, t := range types {
		namespaces = append(namespaces, s.Namespace(t))
	}
	return dedupeSorted(namespaces)
}

// Owns reports whether a namespace belongs to this scope. It is the guard used before mutating a
// row addressed by ID (the ID itself says nothing about who may touch it).
func (s Scope) Owns(namespace string) bool {
	if s.IsGlobal() {
		return !strings.HasPrefix(namespace, "tenant/")
	}
	return strings.HasPrefix(namespace, "tenant/"+s.TenantID+"/user/"+s.UserID+"/")
}

// DedupeKey scopes a caller-supplied idempotency key to the domain in which the namespace is
// unique: transient rows are unique per session+agent, the shared namespaces per user.
func (s Scope) DedupeKey(nsType model.NamespaceType, key string) string {
	key = strings.TrimSpace(key)
	if key == "" || s.IsGlobal() {
		return key
	}
	if nsType == model.NamespaceTypeTransient {
		return strings.Join([]string{s.TenantID, s.UserID, s.SessionID, s.AgentID, key}, ":")
	}
	return strings.Join([]string{s.TenantID, s.UserID, key}, ":")
}

// ScopeFromNamespace reverses Scope.Namespace. It exists for one purpose: re-deriving the scope
// of an existing row so that a follow-up write stays inside the same tenant.
func ScopeFromNamespace(namespace string) (Scope, bool) {
	parts := strings.Split(strings.TrimSpace(namespace), "/")
	if len(parts) < 4 || parts[0] != "tenant" || parts[2] != "user" {
		return Scope{}, false
	}
	scope := Scope{TenantID: parts[1], UserID: parts[3]}
	for i := 4; i+1 < len(parts); i += 2 {
		switch parts[i] {
		case "session":
			scope.SessionID = parts[i+1]
		case "agent":
			scope.AgentID = parts[i+1]
		}
	}
	return scope, true
}

// defaultNamespace maps a namespace type to its global namespace.
func defaultNamespace(nsType model.NamespaceType) string {
	switch nsType {
	case model.NamespaceTypeProfile:
		return "profile/default"
	case model.NamespaceTypeAction:
		return "action/default"
	case model.NamespaceTypeKnowledge:
		return "knowledge/default"
	default:
		return "transient/default"
	}
}

// inferScopeNamespaceType reads the namespace type back out of a namespace string, in both the
// global and the tenant-prefixed layout. Policy resolution depends on it: an isolated namespace
// that is mis-detected silently receives the wrong TTL and rank weights.
func inferScopeNamespaceType(namespace string) model.NamespaceType {
	ns := strings.TrimSpace(namespace)
	if strings.HasPrefix(ns, "tenant/") {
		if idx := strings.LastIndex(ns, "/"); idx >= 0 && idx < len(ns)-1 {
			ns = ns[idx+1:]
		}
	}
	switch {
	case strings.HasPrefix(ns, string(model.NamespaceTypeProfile)):
		return model.NamespaceTypeProfile
	case strings.HasPrefix(ns, string(model.NamespaceTypeAction)):
		return model.NamespaceTypeAction
	case strings.HasPrefix(ns, string(model.NamespaceTypeKnowledge)):
		return model.NamespaceTypeKnowledge
	case strings.HasPrefix(ns, string(model.NamespaceTypeTransient)):
		return model.NamespaceTypeTransient
	default:
		return ""
	}
}

// validNamespaceType reports whether a namespace type is one of the four known kinds.
func validNamespaceType(nsType model.NamespaceType) bool {
	switch nsType {
	case model.NamespaceTypeTransient, model.NamespaceTypeProfile, model.NamespaceTypeAction, model.NamespaceTypeKnowledge:
		return true
	default:
		return false
	}
}

// normalizeNamespaceType fills in the default type for a request.
func normalizeNamespaceType(nsType model.NamespaceType) (model.NamespaceType, error) {
	if nsType == "" {
		return model.NamespaceTypeTransient, nil
	}
	if !validNamespaceType(nsType) {
		return "", newErr(CodeValidation, fmt.Sprintf("invalid namespace_type %q (want transient|profile|action|knowledge)", nsType))
	}
	return nsType, nil
}

func dedupeSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
