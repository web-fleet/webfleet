package scheduler

import (
	"context"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/web-fleet/webfleet/internal/crawler"
	"github.com/web-fleet/webfleet/internal/dnsobs"
	"github.com/web-fleet/webfleet/internal/monitor"
	"github.com/web-fleet/webfleet/internal/store"
	"github.com/web-fleet/webfleet/internal/tlshealth"
)

type Checker interface {
	CheckSite(context.Context, int64) (monitor.Result, error)
}

type Scheduler struct {
	store         *store.Store
	checker       Checker
	tls           *tlshealth.Service
	dns           *dnsobs.Service
	crawler       *crawler.Service
	interval      time.Duration
	crawlInterval time.Duration
	concurrency   int
	log           *slog.Logger
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

func New(st *store.Store, c Checker, tlsSvc *tlshealth.Service, dnsSvc *dnsobs.Service, crawlSvc *crawler.Service, interval, crawlInterval time.Duration, concurrency int, log *slog.Logger) *Scheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	if crawlInterval <= 0 {
		crawlInterval = 6 * time.Hour
	}
	if concurrency < 1 {
		concurrency = 1
	}
	return &Scheduler{store: st, checker: c, tls: tlsSvc, dns: dnsSvc, crawler: crawlSvc, interval: interval, crawlInterval: crawlInterval, concurrency: concurrency, log: log}
}

func (s *Scheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(2)
	go s.loop(ctx, s.interval, s.RunOnce)
	go s.loop(ctx, s.crawlInterval, s.RunCrawlOnce)
}

func (s *Scheduler) loop(ctx context.Context, interval time.Duration, run func(context.Context)) {
	defer s.wg.Done()
	timer := time.NewTimer(jitter(interval))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			run(ctx)
			timer.Reset(jitter(interval))
		}
	}
}

func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

func (s *Scheduler) siteIDs() ([]int64, error) {
	rows, err := sqlite.Query(s.store.DB, `SELECT id FROM sites WHERE enabled=1 AND archived_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r["id"].Int64)
	}
	return ids, nil
}

func (s *Scheduler) RunOnce(ctx context.Context) {
	ids, err := s.siteIDs()
	if err != nil {
		s.log.Error("scheduler list failed", "error", err)
		return
	}
	s.runBounded(ctx, ids, s.concurrency, func(id int64) {
		if _, err := s.checker.CheckSite(ctx, id); err != nil {
			s.log.Warn("site check failed", "site_id", id, "error", err)
		}
		if s.tls != nil && s.observationDue(`SELECT checked_at FROM tls_observations WHERE site_id=? ORDER BY id DESC LIMIT 1`, id, 12*time.Hour) {
			if _, err := s.tls.InspectSite(ctx, id); err != nil {
				s.log.Warn("tls inspection failed", "site_id", id, "error", err)
			}
		}
		if s.dns != nil && s.observationDue(`SELECT checked_at FROM dns_observations WHERE site_id=? ORDER BY id DESC LIMIT 1`, id, time.Hour) {
			if _, err := s.dns.ObserveSite(ctx, id); err != nil {
				s.log.Warn("dns observation failed", "site_id", id, "error", err)
			}
		}
	})
}

func (s *Scheduler) RunCrawlOnce(ctx context.Context) {
	if s.crawler == nil {
		return
	}
	ids, err := s.siteIDs()
	if err != nil {
		s.log.Error("crawler scheduler list failed", "error", err)
		return
	}
	workers := s.concurrency / 2
	if workers < 1 {
		workers = 1
	}
	if workers > 4 {
		workers = 4
	}
	s.runBounded(ctx, ids, workers, func(id int64) {
		if _, err := s.crawler.CrawlSite(ctx, id); err != nil {
			s.log.Warn("site crawl failed", "site_id", id, "error", err)
		}
	})
}

func (s *Scheduler) observationDue(query string, siteID int64, maxAge time.Duration) bool {
	rows, _ := sqlite.Query(s.store.DB, query, siteID)
	if len(rows) == 0 {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, rows[0]["checked_at"].Text)
	return err != nil || time.Since(t) > maxAge
}

func (s *Scheduler) runBounded(ctx context.Context, ids []int64, limit int, fn func(int64)) {
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			fn(id)
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
