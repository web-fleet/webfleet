package maintenance

import (
	"errors"
	"github.com/webfleet-cv/webfleet/internal/sqlite"
	"github.com/webfleet-cv/webfleet/internal/store"
	"os"
	"time"
)

type Settings struct {
	CheckDays        int `json:"check_days"`
	AnalyticsRawDays int `json:"analytics_raw_days"`
	AuditDays        int `json:"audit_days"`
}
type Status struct {
	Settings        Settings `json:"settings"`
	DatabaseBytes   int64    `json:"database_bytes"`
	Checks          int64    `json:"checks"`
	AnalyticsEvents int64    `json:"analytics_events"`
	AuditRuns       int64    `json:"audit_runs"`
}
type Service struct{ st *store.Store }

func New(st *store.Store) *Service { return &Service{st: st} }
func (s *Service) Settings() (Settings, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT check_days,analytics_raw_days,audit_days FROM maintenance_settings WHERE id=1`)
	if e != nil || len(r) == 0 {
		return Settings{}, e
	}
	x := r[0]
	return Settings{int(x["check_days"].Int64), int(x["analytics_raw_days"].Int64), int(x["audit_days"].Int64)}, nil
}
func (s *Service) Set(v Settings) error {
	if v.CheckDays < 7 || v.AnalyticsRawDays < 1 || v.AuditDays < 1 {
		return errors.New("retention values are below safe minimums")
	}
	return sqlite.Exec(s.st.DB, `UPDATE maintenance_settings SET check_days=?,analytics_raw_days=?,audit_days=?,updated_at=? WHERE id=1`, v.CheckDays, v.AnalyticsRawDays, v.AuditDays, store.Now())
}
func (s *Service) Run() error {
	v, e := s.Settings()
	if e != nil {
		return e
	}
	cut := func(days int) string { return time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339Nano) }
	if e = sqlite.Exec(s.st.DB, `DELETE FROM check_results WHERE checked_at<?`, cut(v.CheckDays)); e != nil {
		return e
	}
	if e = sqlite.Exec(s.st.DB, `DELETE FROM analytics_events WHERE occurred_at<?`, cut(v.AnalyticsRawDays)); e != nil {
		return e
	}
	if e = sqlite.Exec(s.st.DB, `DELETE FROM audit_runs WHERE created_at<? AND site_id IN (SELECT site_id FROM audit_settings WHERE history_enabled=1)`, cut(v.AuditDays)); e != nil {
		return e
	}
	return nil
}
func (s *Service) Status() (Status, error) {
	v, e := s.Settings()
	if e != nil {
		return Status{}, e
	}
	out := Status{Settings: v}
	for q, dst := range map[string]*int64{`SELECT COUNT(*) FROM check_results`: &out.Checks, `SELECT COUNT(*) FROM analytics_events`: &out.AnalyticsEvents, `SELECT COUNT(*) FROM audit_runs`: &out.AuditRuns} {
		r, e := sqlite.Query(s.st.DB, q)
		if e != nil {
			return out, e
		}
		*dst = r[0]["COUNT(*)"].Int64
	}
	if s.st.Dialect() == "sqlite" {
		if fi, e := os.Stat(s.st.Path()); e == nil {
			out.DatabaseBytes = fi.Size()
		}
	}
	return out, nil
}
