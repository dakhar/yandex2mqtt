package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-oauth2/oauth2/v4"
	"github.com/go-oauth2/oauth2/v4/errors"
	"github.com/go-oauth2/oauth2/v4/manage"
	"github.com/go-oauth2/oauth2/v4/models"
	"github.com/go-oauth2/oauth2/v4/server"
	"github.com/go-oauth2/oauth2/v4/store"

	"github.com/dakhar/yandex2mqtt/internal/config"
	"github.com/dakhar/yandex2mqtt/internal/yandex"
)

// OAuth is the OAuth2 authorization server: authorization_code, password,
// client_credentials and refresh grants, backed by a persistent token store.
type OAuth struct {
	srv      *server.Server
	sessions *SessionManager
	log      *slog.Logger
}

// NewOAuth builds the authorization server. tokenStore persists tokens (SQLite),
// sm resolves the logged-in user for the authorization_code flow.
func NewOAuth(cfg *config.Config, tokenStore oauth2.TokenStore, sm *SessionManager, log *slog.Logger) *OAuth {
	if log == nil {
		log = slog.Default()
	}

	manager := manage.NewDefaultManager()
	manager.MapTokenStorage(tokenStore)

	// Long-lived access tokens (30 days) with refresh, so a home bridge rarely
	// needs to re-link. Replaces the never-expiring lokijs tokens.
	tokenCfg := &manage.Config{
		AccessTokenExp:    30 * 24 * time.Hour,
		RefreshTokenExp:   90 * 24 * time.Hour,
		IsGenerateRefresh: true,
	}
	manager.SetAuthorizeCodeTokenCfg(tokenCfg)
	manager.SetPasswordTokenCfg(tokenCfg)
	manager.SetClientTokenCfg(&manage.Config{AccessTokenExp: 30 * 24 * time.Hour})

	// The original validated nothing about redirect_uri ("you have been
	// warned"); we allow any redirect to keep parity with the Yandex flow.
	manager.SetValidateURIHandler(func(_ string, _ string) error { return nil })

	clientStore := store.NewClientStore()
	_ = clientStore.Set(cfg.OAuth.ClientID, &models.Client{
		ID:     cfg.OAuth.ClientID,
		Secret: cfg.OAuth.ClientSecret,
	})
	manager.MapClientStorage(clientStore)

	sc := server.NewConfig()
	sc.AllowGetAccessRequest = true
	sc.AllowedGrantTypes = []oauth2.GrantType{
		oauth2.AuthorizationCode,
		oauth2.PasswordCredentials,
		oauth2.ClientCredentials,
		oauth2.Refreshing,
	}
	srv := server.NewServer(sc, manager)

	o := &OAuth{srv: srv, sessions: sm, log: log}

	// Accept client credentials from either HTTP Basic or the request body.
	srv.SetClientInfoHandler(func(r *http.Request) (string, string, error) {
		if id, secret, ok := r.BasicAuth(); ok {
			return id, secret, nil
		}
		return server.ClientFormHandler(r)
	})

	// Password grant: validate against the configured admin user.
	srv.SetPasswordAuthorizationHandler(func(_ context.Context, _, username, password string) (string, error) {
		if sm.verify(username, password) {
			return sm.admin.ID, nil
		}
		return "", errors.ErrInvalidGrant
	})

	// Authorization-code grant: resolve the user from the login session,
	// redirecting to /login when not yet authenticated.
	srv.SetUserAuthorizationHandler(func(w http.ResponseWriter, r *http.Request) (string, error) {
		if uid := sm.UserID(r); uid != "" {
			return uid, nil
		}
		http.Redirect(w, r, "/login?redirect="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return "", nil
	})

	srv.SetInternalErrorHandler(func(err error) *errors.Response {
		o.log.Error("oauth internal", "err", err)
		return nil
	})
	srv.SetResponseErrorHandler(func(re *errors.Response) {
		o.log.Debug("oauth response error", "err", re.Error.Error())
	})

	return o
}

// Authorize handles GET /dialog/authorize.
func (o *OAuth) Authorize(w http.ResponseWriter, r *http.Request) {
	if err := o.srv.HandleAuthorizeRequest(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// Token handles POST /oauth/token.
func (o *OAuth) Token(w http.ResponseWriter, r *http.Request) {
	if err := o.srv.HandleTokenRequest(w, r); err != nil {
		o.log.Error("token request", "err", err)
	}
}

// Bearer is the authentication middleware for the provider API: it validates the
// access token and puts the owning user id into the request context.
func (o *OAuth) Bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ti, err := o.srv.ValidationBearerToken(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := yandex.WithUserID(r.Context(), ti.GetUserID())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
