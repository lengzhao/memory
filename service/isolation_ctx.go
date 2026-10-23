package service

import (
	"context"
	"sort"
	"strings"

	"github.com/lengzhao/memory/model"
)

type ctxKey string

const (
	ctxKeyTenantID    ctxKey = "tenant_id"
	ctxKeyUserID      ctxKey = "user_id"
	ctxKeySessionID   ctxKey = "session_id"
	ctxKeyAgentID     ctxKey = "agent_id"
	ctxKeyAllowGlobal ctxKey = "allow_global"
)

// IsolationMeta carries isolation identifiers extracted from context.
type IsolationMeta struct {
	TenantID  string
	UserID    string
	SessionID string
	AgentID   string
}

// WithIsolation sets the isolation identifiers for one call.
//
// Identifiers are stored verbatim: blank values are no longer replaced with "default".
// A partial identity (e.g. tenant + user but no session/agent) fails closed with
// CodeValidation on the next Remember/Recall/List/Forget call, and an empty context is
// rejected with CodeUnauthorized unless AllowGlobal was used. This is deliberate: a host
// that forgets to pass an identity must not silently read or write another user's data.
func WithIsolation(ctx context.Context, tenantID, userID, sessionID, agentID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyTenantID, strings.TrimSpace(tenantID))
	ctx = context.WithValue(ctx, ctxKeyUserID, strings.TrimSpace(userID))
	ctx = context.WithValue(ctx, ctxKeySessionID, strings.TrimSpace(sessionID))
	ctx = context.WithValue(ctx, ctxKeyAgentID, strings.TrimSpace(agentID))
	return ctx
}

// AllowGlobal marks the context as allowed to work on the global (non-isolated)
// namespaces ("profile/default", ...). Use it for single-user / local deployments and for
// maintenance jobs. Without it, operations outside an isolation context fail closed.
//
//	ctx := memory.AllowGlobal(context.Background())
func AllowGlobal(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyAllowGlobal, true)
}

// HasIsolation reports whether ctx carries any isolation identifier.
func HasIsolation(ctx context.Context) bool {
	return isolationFromContextLoose(ctx).any()
}

// IsolationFromContext reads the isolation identity carried by ctx.
// It never fails: a missing or partial identity simply yields the values that are present,
// while the memory operations themselves reject an incomplete identity
// (see resolveIsolation). Use it for logging/diagnostics.
func IsolationFromContext(ctx context.Context) IsolationMeta {
	return isolationFromContextLoose(ctx)
}

// resolveIsolation resolves the effective identity for an operation.
// isolated=false means the caller explicitly opted into the global namespace.
func resolveIsolation(ctx context.Context) (meta IsolationMeta, isolated bool, err error) {
	meta = isolationFromContextLoose(ctx)
	if meta.any() {
		if verr := meta.validate(); verr != nil {
			return IsolationMeta{}, false, verr
		}
		return meta, true, nil
	}
	if allowGlobal(ctx) {
		return IsolationMeta{}, false, nil
	}
	return IsolationMeta{}, false, newErr(CodeUnauthorized,
		"no isolation context: use WithIsolation(ctx, tenantID, userID, sessionID, agentID), or AllowGlobal(ctx) for the global namespace")
}

func (m IsolationMeta) any() bool {
	return m.TenantID != "" || m.UserID != "" || m.SessionID != "" || m.AgentID != ""
}

func (m IsolationMeta) validate() error {
	var missing []string
	if m.TenantID == "" {
		missing = append(missing, "tenant_id")
	}
	if m.UserID == "" {
		missing = append(missing, "user_id")
	}
	if m.SessionID == "" {
		missing = append(missing, "session_id")
	}
	if m.AgentID == "" {
		missing = append(missing, "agent_id")
	}
	if len(missing) > 0 {
		return newErr(CodeValidation,
			"incomplete isolation context, missing "+strings.Join(missing, ",")+
				" (blank identifiers are no longer replaced with \"default\")")
	}
	return nil
}

// deriveIsolationContext reconstructs an isolation context from an explicit namespace.
// It exists only to keep engines that receive a namespace (not a context) — such as the
// decision engine after a merge — scoped correctly. Callers that already carry an identity
// are returned unchanged, and fully qualified namespaces are read back into identifiers.
func deriveIsolationContext(ctx context.Context, namespace string) context.Context {
	if HasIsolation(ctx) || allowGlobal(ctx) {
		return ctx
	}
	meta := isolationFromNamespace(namespace)
	if !meta.any() {
		return ctx
	}
	return WithIsolation(ctx, meta.TenantID, meta.UserID, meta.SessionID, meta.AgentID)
}

// isolationFromNamespace reads tenant/user/session/agent back out of a fully qualified
// namespace ("tenant/<t>/user/<u>[/session/<s>/agent/<a>]/<type>").
func isolationFromNamespace(namespace string) IsolationMeta {
	parts := strings.Split(strings.TrimSpace(namespace), "/")
	if len(parts) < 4 || parts[0] != "tenant" || parts[2] != "user" {
		return IsolationMeta{}
	}
	meta := IsolationMeta{TenantID: parts[1], UserID: parts[3]}
	for i := 4; i+1 < len(parts); i += 2 {
		switch parts[i] {
		case "session":
			meta.SessionID = parts[i+1]
		case "agent":
			meta.AgentID = parts[i+1]
		}
	}
	return meta
}

func isolationFromContextLoose(ctx context.Context) IsolationMeta {
	return IsolationMeta{
		TenantID:  strings.TrimSpace(valueFromContext(ctx, ctxKeyTenantID)),
		UserID:    strings.TrimSpace(valueFromContext(ctx, ctxKeyUserID)),
		SessionID: strings.TrimSpace(valueFromContext(ctx, ctxKeySessionID)),
		AgentID:   strings.TrimSpace(valueFromContext(ctx, ctxKeyAgentID)),
	}
}

func isIsolationEnabled(ctx context.Context) bool {
	return isolationFromContextLoose(ctx).any()
}

func allowGlobal(ctx context.Context) bool {
	v, ok := ctx.Value(ctxKeyAllowGlobal).(bool)
	return ok && v
}

func valueFromContext(ctx context.Context, k ctxKey) string {
	if s, ok := ctx.Value(k).(string); ok {
		return s
	}
	return ""
}

// buildNamespace maps an isolation identity + namespace type to an isolated namespace.
// An empty identity maps to the global namespace (used by policy lookups during
// global-mode operations).
func buildNamespace(meta IsolationMeta, nsType model.NamespaceType) string {
	if !meta.any() {
		return buildDefaultNamespace(nsType)
	}
	return buildNamespaceByType(meta.TenantID, meta.UserID, meta.SessionID, meta.AgentID, string(nsType))
}

// buildNamespaceByType is the string-typed namespace layout used by namespace policies.
// Layout: profile/action/knowledge are user-scoped (shared across sessions), transient is
// session+agent-scoped.
func buildNamespaceByType(tenantID, userID, sessionID, agentID, nsType string) string {
	switch model.NamespaceType(nsType) {
	case model.NamespaceTypeProfile:
		return "tenant/" + tenantID + "/user/" + userID + "/profile"
	case model.NamespaceTypeAction:
		return "tenant/" + tenantID + "/user/" + userID + "/action"
	case model.NamespaceTypeKnowledge:
		return "tenant/" + tenantID + "/user/" + userID + "/knowledge"
	default:
		return "tenant/" + tenantID + "/user/" + userID + "/session/" + sessionID + "/agent/" + agentID + "/transient"
	}
}

func buildDefaultNamespace(nsType model.NamespaceType) string {
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

// buildAllowedNamespaces returns every namespace readable by an isolation identity.
// An empty nsTypes list means "all four namespace types".
func buildAllowedNamespaces(meta IsolationMeta, nsTypes []model.NamespaceType) []string {
	if !meta.any() {
		return nil
	}
	types := nsTypes
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
		namespaces = append(namespaces, buildNamespace(meta, t))
	}
	return dedupeStrings(namespaces)
}

func dedupeStrings(values []string) []string {
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
