package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

func setupSessionMiddlewareTest(t *testing.T) *gin.Engine {
	t.Helper()
	if err := store.InitDB(filepath.Join(t.TempDir(), "session.db")); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	if err := store.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	t.Cleanup(store.CloseDB)
	if err := store.CreateUser(&model.User{
		ID: "user", Username: "reader", Password: "hash", Nickname: "Reader", Role: "user",
	}); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	r := gin.New()
	r.GET("/protected", SessionRequired(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return r
}

func performSessionRequest(r *gin.Engine, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	response := httptest.NewRecorder()
	r.ServeHTTP(response, req)
	return response
}

func TestOIDCSessionCannotOutliveAbsoluteExpiry(t *testing.T) {
	r := setupSessionMiddlewareTest(t)
	now := time.Now().UTC()
	absolute := now.Add(-time.Minute)
	if err := store.CreateSession(&model.UserSession{
		ID: "expired-oidc", UserID: "user", ExpiresAt: now.Add(time.Hour), AuthMethod: "oidc",
		AuthenticatedAt: now.Add(-2 * time.Hour), AbsoluteExpiresAt: &absolute,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	response := performSessionRequest(r, "expired-oidc")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired absolute session status = %d, want 401", response.Code)
	}
	session, _, err := store.GetSessionWithUser("expired-oidc")
	if err != nil || session != nil {
		t.Fatalf("expired session was not deleted: %+v, %v", session, err)
	}
}

func TestOIDCSlidingRenewalIsCappedAtAbsoluteExpiry(t *testing.T) {
	r := setupSessionMiddlewareTest(t)
	now := time.Now().UTC()
	absolute := now.Add(2 * time.Hour)
	if err := store.CreateSession(&model.UserSession{
		ID: "bounded-oidc", UserID: "user", ExpiresAt: now.Add(time.Hour), AuthMethod: "oidc",
		AuthenticatedAt: now, AbsoluteExpiresAt: &absolute,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	response := performSessionRequest(r, "bounded-oidc")
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid session status = %d, want 204", response.Code)
	}
	session, _, err := store.GetSessionWithUser("bounded-oidc")
	if err != nil || session == nil {
		t.Fatalf("GetSessionWithUser() = %+v, %v", session, err)
	}
	if session.ExpiresAt.After(absolute.Add(time.Second)) {
		t.Fatalf("sliding expiry %v exceeded absolute expiry %v", session.ExpiresAt, absolute)
	}
}

func TestOIDCSessionRenewalKeepsSecureCookieWhenConfigIsInvalid(t *testing.T) {
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_DISABLE_PASSWORD_LOGIN", "true")
	t.Setenv("OIDC_ISSUER_URL", "https://identity.example.com")
	t.Setenv("OIDC_CLIENT_ID", "client")
	t.Setenv("OIDC_CLIENT_SECRET", "")
	t.Setenv("PUBLIC_URL", "https://reader.example.com")

	r := setupSessionMiddlewareTest(t)
	now := time.Now().UTC()
	absolute := now.Add(2 * time.Hour)
	if err := store.CreateSession(&model.UserSession{
		ID: "secure-oidc", UserID: "user", ExpiresAt: now.Add(time.Hour), AuthMethod: model.SessionAuthMethodOIDC,
		AuthenticatedAt: now, AbsoluteExpiresAt: &absolute,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	response := performSessionRequest(r, "secure-oidc")
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid session status = %d, want 204", response.Code)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == SessionCookie {
			if !cookie.Secure {
				t.Fatal("OIDC renewal downgraded the session cookie after a configuration error")
			}
			return
		}
	}
	t.Fatal("OIDC renewal did not set the session cookie")
}
