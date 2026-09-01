package performance

import (
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"math"
)

type Point struct {
	LatencyMS     int64  `json:"latency_ms"`
	ResponseBytes int64  `json:"response_bytes"`
	CheckedAt     string `json:"checked_at"`
}
type Summary struct {
	Samples               int     `json:"samples"`
	MedianLatencyMS       int64   `json:"median_latency_ms"`
	P95LatencyMS          int64   `json:"p95_latency_ms"`
	MedianResponseBytes   int64   `json:"median_response_bytes"`
	BaselineLatencyMS     int64   `json:"baseline_latency_ms"`
	BaselineResponseBytes int64   `json:"baseline_response_bytes"`
	LatencyRegression     bool    `json:"latency_regression"`
	SizeRegression        bool    `json:"size_regression"`
	History               []Point `json:"history"`
}

func ForSite(st *store.Store, siteID int64, limit int) (Summary, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := sqlite.Query(st.DB, `SELECT latency_ms,response_bytes,checked_at FROM check_results WHERE site_id=? AND ok=1 ORDER BY id DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return Summary{}, err
	}
	out := Summary{Samples: len(rows), History: make([]Point, 0, len(rows))}
	lats := make([]int64, 0, len(rows))
	sizes := make([]int64, 0, len(rows))
	for _, r := range rows {
		lats = append(lats, r["latency_ms"].Int64)
		sizes = append(sizes, r["response_bytes"].Int64)
		out.History = append(out.History, Point{r["latency_ms"].Int64, r["response_bytes"].Int64, r["checked_at"].Text})
	}
	if len(rows) == 0 {
		return out, nil
	}
	sort64(lats)
	sort64(sizes)
	out.MedianLatencyMS = percentile(lats, .5)
	out.P95LatencyMS = percentile(lats, .95)
	out.MedianResponseBytes = percentile(sizes, .5)
	baseN := len(rows)
	if baseN > 20 {
		baseN = 20
	}
	var bl, bs int64
	for i := len(rows) - baseN; i < len(rows); i++ {
		bl += rows[i]["latency_ms"].Int64
		bs += rows[i]["response_bytes"].Int64
	}
	out.BaselineLatencyMS = bl / int64(baseN)
	out.BaselineResponseBytes = bs / int64(baseN)
	out.LatencyRegression = out.BaselineLatencyMS > 0 && out.MedianLatencyMS > out.BaselineLatencyMS*3/2
	out.SizeRegression = out.BaselineResponseBytes > 0 && out.MedianResponseBytes > out.BaselineResponseBytes*3/2
	return out, nil
}
func sort64(a []int64) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}
func percentile(a []int64, p float64) int64 {
	if len(a) == 0 {
		return 0
	}
	i := int(math.Ceil(float64(len(a))*p)) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(a) {
		i = len(a) - 1
	}
	return a[i]
}
