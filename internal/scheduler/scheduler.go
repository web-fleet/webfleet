package scheduler

import (
	"context"
	"github.com/web-fleet/webfleet/internal/monitor"
	"github.com/web-fleet/webfleet/internal/store"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

type Checker interface {
	CheckSite(context.Context, int64) (monitor.Result, error)
}
type Scheduler struct {
	store       *store.Store
	checker     Checker
	interval    time.Duration
	concurrency int
	log         *slog.Logger
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func New(st *store.Store, c Checker, interval time.Duration, concurrency int, log *slog.Logger) *Scheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	if concurrency < 1 {
		concurrency = 1
	}
	return &Scheduler{store: st, checker: c, interval: interval, concurrency: concurrency, log: log}
}
func (s *Scheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(jitter(s.interval))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.RunOnce(ctx)
				timer.Reset(jitter(s.interval))
			}
		}
	}()
}
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}
func (s *Scheduler) RunOnce(ctx context.Context) {
	rows, e := s.store.DB.Query(`SELECT id FROM sites WHERE enabled=1 AND archived_at IS NULL ORDER BY id`)
	if e != nil {
		s.log.Error("scheduler list failed", "error", e)
		return
	}
	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup
	for _, r := range rows {
		id := r["id"].Int64
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			if _, e := s.checker.CheckSite(ctx, id); e != nil {
				s.log.Warn("site check failed", "site_id", id, "error", e)
			}
		}()
	}
	wg.Wait()
}
func jitter(d time.Duration) time.Duration {
	spread := d / 10
	if spread < time.Second {
		spread = time.Second
	}
	return d - spread + time.Duration(rand.Int63n(int64(2*spread)))
}
