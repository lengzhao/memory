package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/lengzhao/memory/model"
)

type ctxKey string

const (
	ctxKeyTenantID  ctxKey = "tenant_id"
	ctxKeyUserID    ctxKey = "user_id"
	ctxKeySessionID ctxKey = "session_id"
	ctxKeyAgentID   ctxKey = "agent_id"

	defaultIsolationValue = "default"
)

// IsolationMeta carries isolation identifiers extracted from context.
type IsolationMeta struct {
	TenantID  string
	UserID    string
	SessionID string
	AgentID   string
}

// WithIsolation sets required isolation identifiers in one call.
func WithIsolation(ctx context.Context, tenantID, userID, sessionID, agentID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyTenantID, valueOrDefault(tenantID))
	ctx = context.WithValue(ctx, ctxKeyUserID, valueOrDefault(userID))
	ctx = context.WithValue(ctx, ctxKeySessionID, valueOrDefault(sessionID))
	ctx = context.WithValue(ctx, ctxKeyAgentID, valueOrDefault(agentID))
	return ctx
}

func IsolationFromContext(ctx context.Context) (IsolationMeta, error) {
	meta := isolationFromContextLoose(ctx)
	if meta.TenantID == "" || meta.UserID == "" || meta.SessionID == "" || meta.AgentID == "" {
		return IsolationMeta{}, newErr(CodeValidation, "missing tenant_id/user_id/session_id/agent_id in context")
	}
	return meta, nil
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
	meta := isolationFromContextLoose(ctx)
	return meta.TenantID != "" || meta.UserID != "" || meta.SessionID != "" || meta.AgentID != ""
}

func valueFromContext(ctx context.Context, k ctxKey) string {
	v := ctx.Value(k)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func valueOrDefault(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return defaultIsolationValue
	}
	return v
}

func buildNamespace(meta IsolationMeta, nsType model.NamespaceType) string {
	switch nsType {
	case model.NamespaceTypeProfile:
		return fmt.Sprintf("tenant/%s/user/%s/profile", meta.TenantID, meta.UserID)
	case model.NamespaceTypeAction:
		return fmt.Sprintf("tenant/%s/user/%s/action", meta.TenantID, meta.UserID)
	case model.NamespaceTypeKnowledge:
		return fmt.Sprintf("tenant/%s/user/%s/knowledge", meta.TenantID, meta.UserID)
	default:
		return fmt.Sprintf(
			"tenant/%s/user/%s/session/%s/agent/%s/transient",
			meta.TenantID, meta.UserID, meta.SessionID, meta.AgentID,
		)
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

func buildAllowedNamespaces(meta IsolationMeta, nsTypes []model.NamespaceType) []string {
	if len(nsTypes) > 0 {
		namespaces := make([]string, 0, len(nsTypes))
		for _, t := range nsTypes {
			namespaces = append(namespaces, buildNamespace(meta, t))
		}
		return dedupeStrings(namespaces)
	}
	return []string{
		buildNamespace(meta, model.NamespaceTypeTransient),
		buildNamespace(meta, model.NamespaceTypeProfile),
		buildNamespace(meta, model.NamespaceTypeAction),
		buildNamespace(meta, model.NamespaceTypeKnowledge),
	}
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
	return out
}

