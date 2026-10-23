package service

import (
	"context"
	"time"
)

// JanitorConfig controls the background maintenance loop.
type JanitorConfig struct {
	// Interval between sweeps. Defaults to 1 hour.
	Interval time.Duration
	// PurgeBefore is the soft-delete age after which rows are archived and removed from
	// memory_items during a sweep. Defaults to 7 * 24h.
	PurgeBefore time.Duration
	// ArchiveRetention is how long archived rows stay in deleted_items before being dropped
	// irreversibly by a sweep. Zero disables archive purging. Defaults to DefaultDeletedRetention.
	ArchiveRetention time.Duration
	// OnError receives sweep failures (optional). Sweeps never panic and never stop the loop.
	OnError func(error)
}

// Janitor runs the maintenance work that hosts previously had to schedule themselves:
// expiring stale rows (which also prunes the search index) and enforcing the retention
// window of archived deletions.
//
// The work is idempotent, so running several instances against one database is safe.
type Janitor struct {
	svc    *MemoryService
	config JanitorConfig
	cancel context.CancelFunc
	done   chan struct{}
}

// NewJanitor creates a janitor for the given service.
func NewJanitor(svc *MemoryService, cfg JanitorConfig) *Janitor {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.PurgeBefore <= 0 {
		cfg.PurgeBefore = DefaultDeletedRetention
	}
	if cfg.ArchiveRetention == 0 {
		cfg.ArchiveRetention = DefaultDeletedRetention
	}
	return &Janitor{svc: svc, config: cfg, done: make(chan struct{})}
}

// Start launches the loop and returns immediately. Stop cancels it.
func (j *Janitor) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	j.cancel = cancel

	go func() {
		defer close(j.done)
		ticker := time.NewTicker(j.config.Interval)
		defer ticker.Stop()

		// Sweep once at startup so a host that restarts does not wait a full interval.
		j.sweep(runCtx)

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				j.sweep(runCtx)
			}
		}
	}()
}

// Stop cancels the loop and waits for the current sweep to finish.
func (j *Janitor) Stop() {
	if j.cancel == nil {
		return
	}
	j.cancel()
	<-j.done
	j.cancel = nil
}

// SweepOnce runs a single maintenance pass (also useful in cron-style deployments).
func (j *Janitor) SweepOnce(ctx context.Context) error {
	return j.sweepErr(ctx)
}

func (j *Janitor) sweep(ctx context.Context) {
	if err := j.sweepErr(ctx); err != nil && j.config.OnError != nil {
		j.config.OnError(err)
	}
}

func (j *Janitor) sweepErr(ctx context.Context) error {
	now := time.Now()

	if _, err := j.svc.CleanupExpired(ctx); err != nil {
		return err
	}
	if _, err := j.svc.PurgeDeleted(ctx, now.Add(-j.config.PurgeBefore)); err != nil {
		return err
	}
	if j.config.ArchiveRetention > 0 {
		if _, err := j.svc.PurgeExpiredDeleted(ctx, now); err != nil {
			return err
		}
	}
	return nil
}
