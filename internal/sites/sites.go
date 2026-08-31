package sites

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
)

type Service struct{ store *store.Store }
type Group struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SiteCount int64  `json:"site_count"`
}
type Site struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PrimaryURL string `json:"primary_url"`
	Enabled    bool   `json:"enabled"`
	GroupID    int64  `json:"group_id"`
	GroupName  string `json:"group_name"`
	Archived   bool   `json:"archived"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}
type List struct {
	Sites                        []Site `json:"sites"`
	Page, PageSize, Total, Pages int    `json:"page"`
}

func New(s *store.Store) *Service { return &Service{store: s} }
func CanonicalURL(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil {
		return "", errors.New("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("URL must use http or https")
	}
	if u.Hostname() == "" || u.User != nil {
		return "", errors.New("URL must contain a hostname and no credentials")
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}
func (s *Service) CreateGroup(name string) (Group, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return Group{}, errors.New("group name must be 1..80 characters")
	}
	r, e := s.store.DB.Query(`INSERT INTO groups(name,created_at) VALUES(?,?) RETURNING id`, name, store.Now())
	if e != nil {
		return Group{}, e
	}
	return Group{ID: r[0]["id"].Int64, Name: name}, nil
}
func (s *Service) Groups() ([]Group, error) {
	r, e := s.store.DB.Query(`SELECT g.id,g.name,COUNT(s.id) site_count FROM groups g LEFT JOIN sites s ON s.group_id=g.id AND s.archived_at IS NULL GROUP BY g.id ORDER BY lower(g.name)`)
	if e != nil {
		return nil, e
	}
	out := make([]Group, 0, len(r))
	for _, x := range r {
		out = append(out, Group{ID: x["id"].Int64, Name: x["name"].Text, SiteCount: x["site_count"].Int64})
	}
	return out, nil
}
func (s *Service) Create(name, rawURL string, groupID int64) (Site, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return Site{}, errors.New("site name must be 1..120 characters")
	}
	u, e := CanonicalURL(rawURL)
	if e != nil {
		return Site{}, e
	}
	if groupID > 0 {
		r, e := s.store.DB.Query(`SELECT id FROM groups WHERE id=?`, groupID)
		if e != nil || len(r) == 0 {
			return Site{}, errors.New("group not found")
		}
	}
	now := store.Now()
	r, e := s.store.DB.Query(`INSERT INTO sites(organization_id,name,primary_url,enabled,group_id,created_at,updated_at) VALUES(1,?,?,1,NULLIF(?,0),?,?) RETURNING id`, name, u, groupID, now, now)
	if e != nil {
		return Site{}, e
	}
	return s.Get(r[0]["id"].Int64)
}
func (s *Service) Get(id int64) (Site, error) {
	r, e := s.store.DB.Query(`SELECT s.id,s.name,s.primary_url,s.enabled,COALESCE(s.group_id,0) group_id,COALESCE(g.name,'') group_name,s.archived_at,s.created_at,s.updated_at FROM sites s LEFT JOIN groups g ON g.id=s.group_id WHERE s.id=?`, id)
	if e != nil || len(r) == 0 {
		return Site{}, errors.New("site not found")
	}
	return rowSite(r[0]), nil
}
func (s *Service) Update(id int64, name, rawURL string, groupID int64, enabled bool) (Site, error) {
	if _, e := s.Get(id); e != nil {
		return Site{}, e
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return Site{}, errors.New("site name must be 1..120 characters")
	}
	u, e := CanonicalURL(rawURL)
	if e != nil {
		return Site{}, e
	}
	if e = s.store.DB.Exec(`UPDATE sites SET name=?,primary_url=?,group_id=NULLIF(?,0),enabled=?,updated_at=? WHERE id=?`, name, u, groupID, enabled, store.Now(), id); e != nil {
		return Site{}, e
	}
	return s.Get(id)
}
func (s *Service) Archive(id int64, archived bool) error {
	var at any = nil
	if archived {
		at = store.Now()
	}
	return s.store.DB.Exec(`UPDATE sites SET archived_at=?,updated_at=? WHERE id=?`, at, store.Now(), id)
}
func (s *Service) Delete(id int64) error {
	site, e := s.Get(id)
	if e != nil {
		return e
	}
	if !site.Archived {
		return errors.New("archive site before deleting it")
	}
	return s.store.DB.Exec(`DELETE FROM sites WHERE id=?`, id)
}
func (s *Service) List(q string, groupID int64, page, pageSize int, includeArchived bool) (List, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	where := ` WHERE 1=1`
	args := []any{}
	if !includeArchived {
		where += ` AND s.archived_at IS NULL`
	}
	if strings.TrimSpace(q) != "" {
		where += ` AND (lower(s.name) LIKE ? OR lower(s.primary_url) LIKE ?)`
		term := "%" + strings.ToLower(strings.TrimSpace(q)) + "%"
		args = append(args, term, term)
	}
	if groupID > 0 {
		where += ` AND s.group_id=?`
		args = append(args, groupID)
	}
	cr, e := s.store.DB.Query(`SELECT COUNT(*) n FROM sites s`+where, args...)
	if e != nil {
		return List{}, e
	}
	total := int(cr[0]["n"].Int64)
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, e := s.store.DB.Query(`SELECT s.id,s.name,s.primary_url,s.enabled,COALESCE(s.group_id,0) group_id,COALESCE(g.name,'') group_name,s.archived_at,s.created_at,s.updated_at FROM sites s LEFT JOIN groups g ON g.id=s.group_id`+where+` ORDER BY lower(s.name) LIMIT ? OFFSET ?`, args...)
	if e != nil {
		return List{}, e
	}
	out := List{Sites: make([]Site, 0, len(rows)), Page: page, PageSize: pageSize, Total: total, Pages: pages}
	for _, r := range rows {
		out.Sites = append(out.Sites, rowSite(r))
	}
	return out, nil
}
func rowSite(r sqlite.Row) Site {
	return Site{ID: r["id"].Int64, Name: r["name"].Text, PrimaryURL: r["primary_url"].Text, Enabled: r["enabled"].Int64 != 0, GroupID: r["group_id"].Int64, GroupName: r["group_name"].Text, Archived: !r["archived_at"].Null, CreatedAt: r["created_at"].Text, UpdatedAt: r["updated_at"].Text}
}
func ParseID(raw string) (int64, error) {
	id, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || id < 1 {
		return 0, fmt.Errorf("invalid id")
	}
	return id, nil
}
