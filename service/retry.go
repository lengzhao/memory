package service

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// sqliteBusyRetryAttempts / sqliteBusyRetryBaseDelay control the write retry loop.
// SQLite serializes writers; with several connections (or host processes) a write can fail
// with SQLITE_BUSY even when busy_timeout was exceeded. Retrying with a small backoff turns
// that into a slow write instead of a hard error the host has to understand.
const (
	sqliteBusyRetryAttempts  = 5
	sqliteBusyRetryBaseDelay = 20 * time.Millisecond
)

// withBusyRetry runs fn, retrying while SQLite reports a busy/locked database.
func withBusyRetry(ctx context.Context, fn func() error) error {
	var err error
	delay := sqliteBusyRetryBaseDelay
	for attempt := 0; attempt < sqliteBusyRetryAttempts; attempt++ {
		err = fn()
		if err == nil || !isBusyError(err) {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return err
}

func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_busy")
}

// createRow is a retry-aware Create for write paths.
func createRow(ctx context.Context, db *gorm.DB, value interface{}) error {
	return withBusyRetry(ctx, func() error {
		return db.WithContext(ctx).Create(value).Error
	})
}

// updateRows is a retry-aware Updates for write paths; it returns the affected row count so
// callers keep their conflict detection semantics.
func updateRows(ctx context.Context, query *gorm.DB, updates map[string]interface{}) (int64, error) {
	var affected int64
	err := withBusyRetry(ctx, func() error {
		result := query.WithContext(ctx).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		affected = result.RowsAffected
		return nil
	})
	return affected, err
}
