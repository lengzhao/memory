package service

import (
	"context"
	"strings"
	"time"

	"github.com/lengzhao/memory/model"
)

type ctxKey string

const (
	ctxKeyActorID   ctxKey = "actor_id"
	ctxKeyTraceID   ctxKey = "trace_id"
	ctxKeyRequestID ctxKey = "request_id"
)

// WithActor records who performs an operation (user id, agent id, "system"). The value is written
// to memory_events when Config.RecordEvents is enabled. Audit metadata is the only thing carried
// in the context: isolation is an explicit argument everywhere.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, ctxKeyActorID, strings.TrimSpace(actor))
}

// WithTrace attaches trace/request identifiers for the audit trail.
func WithTrace(ctx context.Context, traceID, requestID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyTraceID, strings.TrimSpace(traceID))
	return context.WithValue(ctx, ctxKeyRequestID, strings.TrimSpace(requestID))
}

func actorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyActorID).(string); ok {
		return v
	}
	return ""
}

func traceFromContext(ctx context.Context) (string, string) {
	var traceID, requestID string
	if v, ok := ctx.Value(ctxKeyTraceID).(string); ok {
		traceID = v
	}
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		requestID = v
	}
	return traceID, requestID
}

// recordEvent writes one audit row. It is a no-op unless Config.RecordEvents is enabled and never
// fails the surrounding operation: losing an audit entry is logged, not propagated.
func (s *MemoryService) recordEvent(ctx context.Context, eventType model.EventType, itemID *string, namespace string, payload map[string]interface{}) {
	if !s.config.RecordEvents {
		return
	}

	event := model.MemoryEvent{
		ID:        model.GenerateID(),
		ItemID:    itemID,
		EventType: eventType,
		Namespace: namespace,
		Actor:     optionalString(actorFromContext(ctx)),
		CreatedAt: time.Now(),
	}
	traceID, requestID := traceFromContext(ctx)
	event.TraceID = optionalString(traceID)
	event.RequestID = optionalString(requestID)
	if len(payload) > 0 {
		event.PayloadJSON = optionalString(toJSON(payload))
	}

	if err := s.db.WithContext(ctx).Create(&event).Error; err != nil {
		GetLogger().Warn("failed to record memory event", "error", err, "event_type", string(eventType))
	}
}
