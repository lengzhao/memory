package service

import (
	"context"
	"strings"
	"time"

	"github.com/lengzhao/memory/model"
)

type ctxKeyActor ctxKey
type ctxKeyTrace ctxKey

const (
	ctxKeyActorID   = ctxKeyActor("actor_id")
	ctxKeyTraceID   = ctxKeyTrace("trace_id")
	ctxKeyRequestID = ctxKeyTrace("request_id")
	// ctxKeyTagsAnyEsc is unused; kept out of the public surface.
)

// WithActor records who is performing the operation (user id, agent id, "system").
// It is written to memory_events when Config.RecordEvents is enabled.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, ctxKeyActorID, strings.TrimSpace(actor))
}

// WithTrace attaches trace/request identifiers for the audit trail
// (written when Config.RecordEvents is enabled).
func WithTrace(ctx context.Context, traceID, requestID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyTraceID, strings.TrimSpace(traceID))
	ctx = context.WithValue(ctx, ctxKeyRequestID, strings.TrimSpace(requestID))
	return ctx
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

// recordEvent writes one audit row. It is a no-op unless Config.RecordEvents is enabled,
// and it never fails the surrounding operation: audit loss is logged, not propagated.
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
