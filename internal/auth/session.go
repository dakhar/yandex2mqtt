// Package auth provides session-based login, application-user management, and
// the OAuth2 authorization server (backed by go-oauth2). It replaces the
// passport/oauth2orize stack and the hardcoded session secret of the original.
package auth

import (
	"context"
	"net/http"

	"github.com/gorilla/sessions"

	"github.com/dakhar/yandex2mqtt/internal/store"
)

const sessionName = "y2m_session"

// SessionManager handles the login cookie session and authenticates users
// against the database.
type SessionManager struct {
	store *sessions.CookieStore
	users *store.UserRepo
}

// NewSessionManager builds a cookie-backed session manager. secret comes from
// config (env), replacing the original hardcoded "keyboard cat".
func NewSessionManager(secret string, users *store.UserRepo) *SessionManager {
	cs := sessions.NewCookieStore([]byte(secret))
	cs.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	return &SessionManager{store: cs, users: users}
}

// UserID returns the logged-in user id, or "" if not authenticated.
func (m *SessionManager) UserID(r *http.Request) string {
	sess, _ := m.store.Get(r, sessionName)
	uid, _ := sess.Values["uid"].(string)
	return uid
}

// CurrentUser resolves the logged-in user, or (nil, nil) if not authenticated.
func (m *SessionManager) CurrentUser(ctx context.Context, r *http.Request) (*store.User, error) {
	id := m.UserID(r)
	if id == "" {
		return nil, nil
	}
	return m.users.ByID(ctx, id)
}

// Authenticate verifies credentials against the users table. It returns
// (nil, nil) when the user is unknown or the password is wrong.
func (m *SessionManager) Authenticate(ctx context.Context, username, password string) (*store.User, error) {
	u, err := m.users.ByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, nil
	}
	ok, err := VerifyPassword(u.PasswordHash, password)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return u, nil
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
