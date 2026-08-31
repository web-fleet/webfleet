package fleet

import (
	"github.com/web-fleet/webfleet/internal/sites"
	"github.com/web-fleet/webfleet/internal/store"
)

type Summary struct {
	Total     int64        `json:"total"`
	Healthy   int64        `json:"healthy"`
	Degraded  int64        `json:"degraded"`
	Warning   int64        `json:"warning"`
	Down      int64        `json:"down"`
	Unknown   int64        `json:"unknown"`
	Attention []sites.Site `json:"attention"`
}

func SummaryFor(st *store.Store) (Summary, error) {
	r, e := st.DB.Query(`SELECT COUNT(*) total,SUM(CASE WHEN h.state='healthy' THEN 1 ELSE 0 END) healthy,SUM(CASE WHEN h.state='degraded' THEN 1 ELSE 0 END) degraded,SUM(CASE WHEN h.state='warning' THEN 1 ELSE 0 END) warning,SUM(CASE WHEN h.state='down' THEN 1 ELSE 0 END) down,SUM(CASE WHEN h.state IS NULL OR h.state='unknown' THEN 1 ELSE 0 END) unknown FROM sites s LEFT JOIN site_health h ON h.site_id=s.id WHERE s.archived_at IS NULL`)
	if e != nil {
		return Summary{}, e
	}
	x := r[0]
	sum := Summary{Total: x["total"].Int64, Healthy: x["healthy"].Int64, Degraded: x["degraded"].Int64, Warning: x["warning"].Int64, Down: x["down"].Int64, Unknown: x["unknown"].Int64}
	list, e := sites.New(st).List("", 0, 1, 100, false)
	if e != nil {
		return sum, e
	}
	for _, s := range list.Sites {
		if s.Health == "down" || s.Health == "warning" || s.Health == "degraded" {
			sum.Attention = append(sum.Attention, s)
		}
	}
	if len(sum.Attention) > 20 {
		sum.Attention = sum.Attention[:20]
	}
	return sum, nil
}
