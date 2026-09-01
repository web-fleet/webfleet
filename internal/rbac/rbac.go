package rbac

import (
	"errors"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"strings"
)

type Member struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}
type Service struct{ st *store.Store }

func New(st *store.Store) *Service { return &Service{st: st} }
func (s *Service) Role(userID, orgID int64) (string, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT role FROM organization_memberships WHERE user_id=? AND organization_id=?`, userID, orgID)
	if e != nil || len(r) == 0 {
		return "", errors.New("membership not found")
	}
	return r[0]["role"].Text, nil
}
func Can(role, action string) bool {
	switch role {
	case "owner":
		return true
	case "admin":
		return action != "organization.delete"
	case "operator":
		return strings.HasPrefix(action, "site.") || strings.HasPrefix(action, "monitor.") || action == "audit.run"
	case "viewer":
		return strings.HasSuffix(action, ".read")
	}
	return false
}
func (s *Service) Members(orgID int64) ([]Member, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT u.id,u.email,m.role FROM organization_memberships m JOIN users u ON u.id=m.user_id WHERE m.organization_id=? ORDER BY lower(u.email)`, orgID)
	if e != nil {
		return nil, e
	}
	out := []Member{}
	for _, x := range r {
		out = append(out, Member{x["id"].Int64, x["email"].Text, x["role"].Text})
	}
	return out, nil
}
func (s *Service) Add(actor, org int64, email, role string) error {
	role = strings.ToLower(role)
	if !map[string]bool{"owner": true, "admin": true, "operator": true, "viewer": true}[role] {
		return errors.New("invalid role")
	}
	r, e := sqlite.Query(s.st.DB, `SELECT id FROM users WHERE lower(email)=lower(?)`, email)
	if e != nil || len(r) == 0 {
		return errors.New("user not found")
	}
	if e = sqlite.Exec(s.st.DB, `INSERT INTO organization_memberships(organization_id,user_id,role,created_at) VALUES(?,?,?,?) ON CONFLICT(organization_id,user_id) DO UPDATE SET role=excluded.role`, org, r[0]["id"].Int64, role, store.Now()); e != nil {
		return e
	}
	return sqlite.Exec(s.st.DB, `INSERT INTO security_audit(actor_user_id,organization_id,action,target,created_at) VALUES(?,?,?,?,?)`, actor, org, "membership.update", email, store.Now())
}
