package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Result struct {
	ID              int64    `json:"id"`
	SiteID          int64    `json:"site_id"`
	Status          string   `json:"status"`
	Performance     int      `json:"performance"`
	Accessibility   int      `json:"accessibility"`
	BestPractices   int      `json:"best_practices"`
	Discoverability int      `json:"discoverability"`
	Findings        []string `json:"findings"`
	DurationMS      int64    `json:"duration_ms"`
	URL             string   `json:"url"`
	Error           string   `json:"error"`
	CreatedAt       string   `json:"created_at"`
}
type Runner interface {
	Run(context.Context, string) (Result, error)
}
type BrowserRunner struct{ Binary string }

func (b BrowserRunner) Run(ctx context.Context, u string) (Result, error) {
	bin := b.Binary
	if bin == "" {
		for _, x := range []string{"chromium", "chromium-browser", "google-chrome"} {
			if p, e := exec.LookPath(x); e == nil {
				bin = p
				break
			}
		}
	}
	if bin == "" {
		return Result{}, errors.New("browser audit runtime is not installed")
	}
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, e := exec.CommandContext(cctx, bin, "--headless", "--disable-gpu", "--no-sandbox", "--dump-dom", u).Output()
	d := time.Since(start).Milliseconds()
	if e != nil {
		return Result{}, fmt.Errorf("browser audit: %w", e)
	}
	html := strings.ToLower(string(out))
	find := []string{}
	a, bp, disc := 100, 100, 100
	if !strings.Contains(html, "<title") {
		disc -= 20
		find = append(find, "Missing document title")
	}
	if !strings.Contains(html, "name=\"description\"") && !strings.Contains(html, "name='description'") {
		disc -= 15
		find = append(find, "Missing meta description")
	}
	if strings.Contains(html, "<img") && !strings.Contains(html, " alt=") {
		a -= 15
		find = append(find, "Images may be missing alt text")
	}
	if !strings.Contains(html, "<html lang=") {
		a -= 10
		find = append(find, "Missing document language")
	}
	if strings.Contains(html, "http://") {
		bp -= 15
		find = append(find, "Rendered document contains insecure HTTP references")
	}
	perf := 100 - int(d/50)
	if perf < 20 {
		perf = 20
	}
	if perf > 100 {
		perf = 100
	}
	return Result{Status: "complete", Performance: perf, Accessibility: a, BestPractices: bp, Discoverability: disc, Findings: find, DurationMS: d, URL: u}, nil
}

type Service struct {
	st     *store.Store
	runner Runner
	sem    chan struct{}
}

func New(st *store.Store) *Service {
	return &Service{st: st, runner: BrowserRunner{}, sem: make(chan struct{}, 2)}
}
func NewWithRunner(st *store.Store, r Runner) *Service {
	return &Service{st: st, runner: r, sem: make(chan struct{}, 2)}
}
func (s *Service) Run(ctx context.Context, siteID int64) (Result, error) {
	rows, e := sqlite.Query(s.st.DB, `SELECT primary_url FROM sites WHERE id=? AND archived_at IS NULL`, siteID)
	if e != nil || len(rows) == 0 {
		return Result{}, errors.New("site not found")
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	res, e := s.runner.Run(ctx, rows[0]["primary_url"].Text)
	res.SiteID = siteID
	res.CreatedAt = store.Now()
	if e != nil {
		res.Status = "failed"
		res.Error = e.Error()
	}
	fj, _ := json.Marshal(res.Findings)
	rr, pe := sqlite.Query(s.st.DB, `INSERT INTO audit_runs(site_id,status,performance_score,accessibility_score,best_practices_score,discoverability_score,findings_json,duration_ms,audited_url,error,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) RETURNING id`, siteID, res.Status, res.Performance, res.Accessibility, res.BestPractices, res.Discoverability, string(fj), res.DurationMS, res.URL, res.Error, res.CreatedAt)
	if pe != nil {
		return Result{}, pe
	}
	res.ID = rr[0]["id"].Int64
	h, _ := s.HistoryEnabled(siteID)
	if !h {
		_ = sqlite.Exec(s.st.DB, `DELETE FROM audit_runs WHERE site_id=? AND id<>?`, siteID, res.ID)
	}
	return res, e
}
func (s *Service) HistoryEnabled(id int64) (bool, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT history_enabled FROM audit_settings WHERE site_id=?`, id)
	if e != nil {
		return false, e
	}
	return len(r) > 0 && r[0]["history_enabled"].Int64 != 0, nil
}
func (s *Service) SetHistory(id int64, on bool) error {
	return sqlite.Exec(s.st.DB, `INSERT INTO audit_settings(site_id,history_enabled,updated_at) VALUES(?,?,?) ON CONFLICT(site_id) DO UPDATE SET history_enabled=excluded.history_enabled,updated_at=excluded.updated_at`, id, on, store.Now())
}
func (s *Service) History(id int64) ([]Result, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT * FROM audit_runs WHERE site_id=? ORDER BY id DESC LIMIT 50`, id)
	if e != nil {
		return nil, e
	}
	out := []Result{}
	for _, x := range r {
		v := Result{ID: x["id"].Int64, SiteID: id, Status: x["status"].Text, Performance: int(x["performance_score"].Int64), Accessibility: int(x["accessibility_score"].Int64), BestPractices: int(x["best_practices_score"].Int64), Discoverability: int(x["discoverability_score"].Int64), DurationMS: x["duration_ms"].Int64, URL: x["audited_url"].Text, Error: x["error"].Text, CreatedAt: x["created_at"].Text}
		_ = json.Unmarshal([]byte(x["findings_json"].Text), &v.Findings)
		out = append(out, v)
	}
	return out, nil
}

type BatchFilter struct {
	Search          string `json:"search"`
	GroupID         int64  `json:"group_id"`
	Regex           string `json:"regex"`
	LastAuditedDays int    `json:"last_audited_days"`
}

func (s *Service) ResolveBatch(f BatchFilter) ([]int64, error) {
	rows, e := sqlite.Query(s.st.DB, `SELECT id,name,primary_url,group_id FROM sites WHERE archived_at IS NULL AND enabled=1 ORDER BY id`)
	if e != nil {
		return nil, e
	}
	var rx *regexp.Regexp
	if strings.TrimSpace(f.Regex) != "" {
		rx, e = regexp.Compile(f.Regex)
		if e != nil {
			return nil, fmt.Errorf("invalid regex: %w", e)
		}
	}
	ids := []int64{}
	q := strings.ToLower(strings.TrimSpace(f.Search))
	for _, r := range rows {
		if f.GroupID > 0 && r["group_id"].Int64 != f.GroupID {
			continue
		}
		hay := r["name"].Text + " " + r["primary_url"].Text
		if q != "" && !strings.Contains(strings.ToLower(hay), q) {
			continue
		}
		if rx != nil && !rx.MatchString(hay) {
			continue
		}
		ids = append(ids, r["id"].Int64)
	}
	return ids, nil
}
func (s *Service) RunBatch(ctx context.Context, ids []int64) []Result {
	out := make([]Result, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			r, e := s.Run(ctx, id)
			if e != nil && r.Error == "" {
				r.Error = e.Error()
			}
			out[i] = r
		}(i, id)
	}
	wg.Wait()
	return out
}
