package rbac

import (
	"errors"
	"strings"

	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
)

type Member struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// Membership is a user's acting role within one organization.
type Membership struct {
	OrgID int64
	Role  string
}

type Service struct{ st *store.Store }

func New(st *store.Store) *Service { return &Service{st: st} }

// Resolve returns the acting membership for a user. The deployment currently
// uses a single-organization model; when a user holds multiple memberships the
// lowest organization id wins deterministically. Multi-organization session
// selection is future work, not a hard-coded organization assumption.
func (s *Service) Resolve(userID int64) (Membership, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT organization_id,role FROM organization_memberships WHERE user_id=? ORDER BY organization_id LIMIT 1`, userID)
	if e != nil || len(r) == 0 {
		return Membership{}, errors.New("membership not found")
	}
	return Membership{OrgID: r[0]["organization_id"].Int64, Role: r[0]["role"].Text}, nil
}

func (s *Service) Role(userID, orgID int64) (string, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT role FROM organization_memberships WHERE user_id=? AND organization_id=?`, userID, orgID)
	if e != nil || len(r) == 0 {
		return "", errors.New("membership not found")
	}
	return r[0]["role"].Text, nil
}

// Can implements the deployment role/action matrix. This is the authoritative
// authorization predicate consulted by the HTTP route guard; it is also
// documented in docs/hardening/route-inventory.json and exercised by the
// route-inventory contract test.
//
//   - owner:   every action.
//   - admin:   every action except organization.delete.
//   - operator: read actions, site/monitor operations, and the operational
//     run actions (audit, crawl, TLS, DNS, analytics management, deployment
//     recording, incident acknowledgement).
//   - viewer:  read actions only.
func Can(role, action string) bool {
	switch role {
	case "owner":
		return true
	case "admin":
		return action != "organization.delete"
	case "operator":
		if strings.HasSuffix(action, ".read") {
			return true
		}
		if strings.HasPrefix(action, "site.") || strings.HasPrefix(action, "monitor.") {
			return true
		}
		switch action {
		case "audit.run", "crawl.run", "tls.run", "dns.run",
			"analytics.manage", "deployments.record", "incidents.acknowledge":
			return true
		}
		return false
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