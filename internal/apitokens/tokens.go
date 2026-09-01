package apitokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"strings"
)

type Service struct{ st *store.Store }

func New(st *store.Store) *Service { return &Service{st: st} }

type Created struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Token  string   `json:"token"`
	Prefix string   `json:"prefix"`
	Scopes []string `json:"scopes"`
}

func hash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func (s *Service) Create(user, org int64, name string, scopes []string) (Created, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Created{}, errors.New("token name required")
	}
	allowed := map[string]bool{"sites:read": true, "sites:write": true, "fleet:read": true, "analytics:read": true, "audit:run": true}
	for _, x := range scopes {
		if !allowed[x] {
			return Created{}, errors.New("unsupported token scope")
		}
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := "wf_" + base64.RawURLEncoding.EncodeToString(b)
	prefix := tok[:10]
	r, e := sqlite.Query(s.st.DB, `INSERT INTO api_tokens(user_id,organization_id,name,token_hash,prefix,scopes,created_at) VALUES(?,?,?,?,?,?,?) RETURNING id`, user, org, name, hash(tok), prefix, strings.Join(scopes, ","), store.Now())
	if e != nil {
		return Created{}, e
	}
	return Created{r[0]["id"].Int64, name, tok, prefix, scopes}, nil
}
// Authenticate validates a token and returns its owner, organization and
// scopes. Unknown and revoked tokens return the same generic error so token
// existence is not disclosed. last_used_at is refreshed on success.
func (s *Service) Authenticate(tok string) (int64, int64, []string, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT id,user_id,organization_id,scopes FROM api_tokens WHERE token_hash=? AND revoked_at IS NULL`, hash(tok))
	if e != nil || len(r) == 0 {
		return 0, 0, nil, errors.New("invalid API token")
	}
	var scopes []string
	if t := r[0]["scopes"].Text; t != "" {
		scopes = strings.Split(t, ",")
	}
	_ = sqlite.Exec(s.st.DB, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, store.Now(), r[0]["id"].Int64)
	return r[0]["user_id"].Int64, r[0]["organization_id"].Int64, scopes, nil
}

// HasScope reports whether the token scope list includes the wanted scope.
func HasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}
func (s *Service) Revoke(id, user, org int64) error {
	return sqlite.Exec(s.st.DB, `UPDATE api_tokens SET revoked_at=? WHERE id=? AND user_id=? AND organization_id=?`, store.Now(), id, user, org)
}
