package auth

import (
	"context"
	"net/http"

	"github.com/dakhar/yandex2mqtt/internal/store"
)

type ctxKey int

const userKey ctxKey = iota

// UserFrom returns the authenticated user placed in the context by RequireLogin.
func UserFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey).(*store.User)
	return u
}

// WithUser returns a context carrying the authenticated user. RequireLogin uses
// it; it is also handy for tests and any handler that resolves the user itself.
func WithUser(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// RequireLogin gates a handler behind a valid session, redirecting to /login
// otherwise, and puts the resolved user into the request context.
func (m *SessionManager) RequireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := m.CurrentUser(r.Context(), r)
		if err != nil {
			http.Error(w, "auth error", http.StatusInternalServerError)
			return
		}
		if u == nil {
			http.Redirect(w, r, "/login?redirect="+r.URL.RequestURI(), http.StatusFound)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

// RequireAdmin gates a handler behind an admin session.
func (m *SessionManager) RequireAdmin(next http.Handler) http.Handler {
	return m.RequireLogin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := UserFrom(r.Context()); u == nil || !u.IsAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}
