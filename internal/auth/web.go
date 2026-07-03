package auth

import (
	"embed"
	"html/template"
	"net/http"
	"net/url"
)

//go:embed templates/*.html
var templatesFS embed.FS

var templates = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

// render executes a page template within the shared layout.
func render(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Clone so we can bind the page's "content" definition into the layout.
	t := template.Must(templates.Clone())
	template.Must(t.ParseFS(templatesFS, "templates/"+page))
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// LoginForm renders the login page (GET /login).
func (m *SessionManager) LoginForm(w http.ResponseWriter, r *http.Request) {
	render(w, "login.html", map[string]any{"Redirect": r.URL.Query().Get("redirect")})
}

// Login handles the login submission (POST /login).
func (m *SessionManager) Login(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	username := r.PostFormValue("username")
	password := r.PostFormValue("password")
	redirect := sanitizeRedirect(r.PostFormValue("redirect"))

	user, err := m.Authenticate(r.Context(), username, password)
	if err != nil {
		http.Error(w, "auth error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		render(w, "login.html", map[string]any{"Redirect": redirect, "Error": "Неверный логин или пароль"})
		return
	}
	if err := m.login(w, r, user.ID); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}

// Logout clears the session (GET /logout).
func (m *SessionManager) Logout(w http.ResponseWriter, r *http.Request) {
	_ = m.logout(w, r)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Account renders the account page, or redirects to login (GET /account).
func (m *SessionManager) Account(w http.ResponseWriter, r *http.Request) {
	u, err := m.CurrentUser(r.Context(), r)
	if err != nil {
		http.Error(w, "user lookup", http.StatusInternalServerError)
		return
	}
	if u == nil {
		http.Redirect(w, r, "/login?redirect=/account", http.StatusFound)
		return
	}
	render(w, "account.html", map[string]any{"Name": u.Name, "ID": u.ID, "IsAdmin": u.IsAdmin})
}

// sanitizeRedirect keeps redirects local to prevent open-redirect abuse.
func sanitizeRedirect(raw string) string {
	if raw == "" {
		return "/app"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "/app"
	}
	if raw[0] != '/' {
		return "/app"
	}
	return raw
}
