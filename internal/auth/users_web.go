package auth

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dakhar/yandex2mqtt/internal/store"
)

// UsersPage renders the admin user-management page (GET /app/users).
func (m *SessionManager) UsersPage(w http.ResponseWriter, r *http.Request) {
	m.renderUsers(w, r, "")
}

func (m *SessionManager) renderUsers(w http.ResponseWriter, r *http.Request, errMsg string) {
	list, err := m.users.List(r.Context())
	if err != nil {
		http.Error(w, "list users", http.StatusInternalServerError)
		return
	}
	render(w, "users.html", map[string]any{
		"Users":   list,
		"Current": UserFrom(r.Context()),
		"Error":   errMsg,
	})
}

// CreateUser handles the create-user form (POST /app/users).
func (m *SessionManager) CreateUser(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	username := strings.TrimSpace(r.PostFormValue("username"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	password := r.PostFormValue("password")
	isAdmin := r.PostFormValue("is_admin") == "on"

	if username == "" || password == "" {
		m.renderUsers(w, r, "Логин и пароль обязательны")
		return
	}
	hash, err := HashPassword(password)
	if err != nil {
		http.Error(w, "hash", http.StatusInternalServerError)
		return
	}
	if _, err := m.users.Create(r.Context(), username, name, hash, isAdmin); err != nil {
		if err == store.ErrUserExists {
			m.renderUsers(w, r, "Пользователь с таким логином уже есть")
			return
		}
		http.Error(w, "create user", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/users", http.StatusFound)
}

// DeleteUser handles user removal (POST /app/users/{id}/delete). It refuses to
// remove the acting user or the last remaining admin.
func (m *SessionManager) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cur := UserFrom(r.Context())
	if cur != nil && id == cur.ID {
		m.renderUsers(w, r, "Нельзя удалить самого себя")
		return
	}
	target, err := m.users.ByID(r.Context(), id)
	if err != nil {
		http.Error(w, "lookup", http.StatusInternalServerError)
		return
	}
	if target != nil && target.IsAdmin {
		n, err := m.users.CountAdmins(r.Context())
		if err != nil {
			http.Error(w, "count admins", http.StatusInternalServerError)
			return
		}
		if n <= 1 {
			m.renderUsers(w, r, "Нельзя удалить последнего администратора")
			return
		}
	}
	if err := m.users.Delete(r.Context(), id); err != nil {
		http.Error(w, "delete", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/users", http.StatusFound)
}
