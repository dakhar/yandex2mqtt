// Package auth provides session-based login and the OAuth2 authorization
// server (backed by go-oauth2). It replaces the passport/oauth2orize stack and
// the hardcoded session secret of the original.
package auth

import (
	"net/http"

	"github.com/gorilla/sessions"

	"github.com/dakhar/yandex2mqtt/internal/config"
)

const sessionName = "y2m_session"

// SessionManager handles the login cookie session.
type SessionManager struct {
	store *sessions.CookieStore
	admin config.Admin
}

// NewSessionManager builds a cookie-backed session manager. secret comes from
// config (env), replacing the original hardcoded "keyboard cat".
func NewSessionManager(secret string, admin config.Admin) *SessionManager {
	cs := sessions.NewCookieStore([]byte(secret))
	cs.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	return &SessionManager{store: cs, admin: admin}
}

// UserID returns the logged-in user id, or "" if not authenticated.
func (m *SessionManager) UserID(r *http.Request) string {
	sess, _ := m.store.Get(r, sessionName)
	uid, _ := sess.Values["uid"].(string)
	return uid
}

// login persists the authenticated user id in the session.
func (m *SessionManager) login(w http.ResponseWriter, r *http.Request, userID string) error {
	sess, _ := m.store.Get(r, sessionName)
	sess.Values["uid"] = userID
	return sess.Save(r, w)
}

// logout clears the session.
func (m *SessionManager) logout(w http.ResponseWriter, r *http.Request) error {
	sess, _ := m.store.Get(r, sessionName)
	delete(sess.Values, "uid")
	sess.Options.MaxAge = -1
	return sess.Save(r, w)
}

// verify checks credentials against the configured admin user.
func (m *SessionManager) verify(username, password string) bool {
	return username == m.admin.Username && password == m.admin.Password
}
