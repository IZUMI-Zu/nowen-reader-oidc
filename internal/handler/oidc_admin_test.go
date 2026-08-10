package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/auth/oidcruntime"
	"github.com/nowen-reader/nowen-reader/internal/middleware"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

func TestOIDCAdminAPIStoresButNeverReturnsClientSecret(t *testing.T) {
	t.Setenv("OIDC_CONFIG_MODE", "database")
	t.Setenv("BASE_PATH", "/reader")
	t.Setenv("DATA_DIR", t.TempDir())
	if err := store.InitDB(filepath.Join(t.TempDir(), "oidc-admin.db")); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	if err := store.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	t.Cleanup(store.CloseDB)

	admin := &model.User{ID: "admin", Username: "admin", Password: "recovery-password-hash", Nickname: "Admin", Role: "admin"}
	if err := store.CreateUser(admin); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if err := store.CreateSession(&model.UserSession{
		ID: "admin-session", UserID: admin.ID, ExpiresAt: time.Now().Add(time.Hour),
		AuthMethod: model.SessionAuthMethodPassword, AuthenticatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	runtime, err := NewOIDCRuntime()
	if err != nil {
		t.Fatalf("NewOIDCRuntime() error = %v", err)
	}
	auth := newAuthHandlerWithRuntime(runtime)
	adminHandler := NewOIDCAdminHandler(runtime, auth)
	router := gin.New()
	group := router.Group("/reader/api/admin/oidc")
	group.Use(middleware.SessionRequired(), middleware.AdminRequired())
	group.GET("", adminHandler.Get)
	group.PUT("", middleware.RequireRecentAuthentication(middleware.RecentAuthenticationWindow), adminHandler.Update)

	body := `{
		"expectedRevision":0,
		"clientSecret":"super-secret-value",
		"config":{
			"enabled":false,
			"issuerURL":"https://identity.example.com",
			"clientID":"nowen-reader",
			"providerName":"Company Login",
			"scopes":["openid","profile","email"],
			"publicURL":"https://reader.example.com",
			"autoProvision":true,
			"sessionTTLSeconds":28800,
			"disablePasswordLogin":false
		}
	}`
	request := httptest.NewRequest(http.MethodPut, "/reader/api/admin/oidc", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "admin-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "super-secret-value") || strings.Contains(response.Body.String(), "secretCiphertext") {
		t.Fatalf("PUT response exposed secret material: %s", response.Body.String())
	}
	var updated map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if updated["clientSecretConfigured"] != true || updated["status"] != "draft" {
		t.Fatalf("unexpected admin configuration: %#v", updated)
	}
	if callback, _ := updated["callbackURL"].(string); callback != "https://reader.example.com/reader/api/auth/oidc/callback" {
		t.Fatalf("callbackURL = %q", callback)
	}
	var contract oidcruntime.AdminConfig
	if err := json.Unmarshal(response.Body.Bytes(), &contract); err != nil {
		t.Fatalf("decode typed admin contract: %v", err)
	}
	if contract.ManagedBy != oidcruntime.ConfigSourceDatabase || !contract.Editable || contract.Revision != 1 ||
		contract.Config.Enabled || contract.Config.IssuerURL != "https://identity.example.com" || contract.Config.ClientID != "nowen-reader" ||
		contract.Config.ProviderName != "Company Login" || strings.Join(contract.Config.Scopes, " ") != "openid profile email" ||
		contract.Config.PublicURL != "https://reader.example.com" || !contract.Config.AutoProvision || contract.Config.SessionTTLSeconds != 28800 ||
		contract.Config.DisablePasswordLogin || contract.CallbackURL != "https://reader.example.com/reader/api/auth/oidc/callback" || contract.BasePath != "/reader" {
		t.Fatalf("UI/API/database field contract mismatch: %+v", contract)
	}

	oidcOnlyUser := &model.User{ID: "oidc-only", Username: "oidc-only", Nickname: "OIDC Only", Role: "user"}
	if err := store.CreateUser(oidcOnlyUser); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkOIDCIdentity(context.Background(), oidcOnlyUser.ID, oidcauth.VerifiedIdentity{
		Issuer: "https://identity.example.com", Subject: "oidc-only-subject",
	}); err != nil {
		t.Fatal(err)
	}
	getRequest := httptest.NewRequest(http.MethodGet, "/reader/api/admin/oidc", nil)
	getRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "admin-session"})
	getResponse := httptest.NewRecorder()
	router.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK || strings.Contains(getResponse.Body.String(), "super-secret-value") || strings.Contains(getResponse.Body.String(), "secretCiphertext") {
		t.Fatalf("GET response = %d: %s", getResponse.Code, getResponse.Body.String())
	}
	if getResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET Cache-Control = %q", getResponse.Header().Get("Cache-Control"))
	}
	var fetched oidcruntime.AdminConfig
	if err := json.Unmarshal(getResponse.Body.Bytes(), &fetched); err != nil || fetched.OIDCOnlyUserCount != 1 {
		t.Fatalf("OIDC-only user impact = %d, %v", fetched.OIDCOnlyUserCount, err)
	}

	for name, invalidBody := range map[string]string{
		"unknown nested field": `{"expectedRevision":1,"config":{"unexpected":true}}`,
		"missing config":       `{"expectedRevision":1}`,
		"missing revision":     `{"config":{"enabled":false}}`,
		"multiple JSON values": `{"expectedRevision":1,"config":{}} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			invalidRequest := httptest.NewRequest(http.MethodPut, "/reader/api/admin/oidc", strings.NewReader(invalidBody))
			invalidRequest.Header.Set("Content-Type", "application/json")
			invalidRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "admin-session"})
			invalidResponse := httptest.NewRecorder()
			router.ServeHTTP(invalidResponse, invalidRequest)
			if invalidResponse.Code != http.StatusBadRequest {
				t.Fatalf("invalid request status = %d: %s", invalidResponse.Code, invalidResponse.Body.String())
			}
		})
	}

	oversizedBody := `{"expectedRevision":1,"clientSecret":"` + strings.Repeat("x", 70<<10) + `","config":{}}`
	oversizedRequest := httptest.NewRequest(http.MethodPut, "/reader/api/admin/oidc", strings.NewReader(oversizedBody))
	oversizedRequest.Header.Set("Content-Type", "application/json")
	oversizedRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "admin-session"})
	oversizedResponse := httptest.NewRecorder()
	router.ServeHTTP(oversizedResponse, oversizedRequest)
	if oversizedResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d: %s", oversizedResponse.Code, oversizedResponse.Body.String())
	}
	var rejectedAuditCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM "OIDCConfigAudit" WHERE "action" = 'update' AND "result" = 'failure:invalid_request'`).Scan(&rejectedAuditCount); err != nil {
		t.Fatalf("load rejected-request audits: %v", err)
	}
	if rejectedAuditCount != 5 {
		t.Fatalf("rejected-request audit count = %d, want 5", rejectedAuditCount)
	}
}

func TestOIDCAdminAPIRequiresAdministratorBrowserSession(t *testing.T) {
	t.Setenv("OIDC_CONFIG_MODE", "database")
	t.Setenv("DATA_DIR", t.TempDir())
	if err := store.InitDB(filepath.Join(t.TempDir(), "oidc-admin-access.db")); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	if err := store.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	t.Cleanup(store.CloseDB)
	user := &model.User{ID: "regular", Username: "regular", Password: "hash", Nickname: "Regular", Role: "user"}
	if err := store.CreateUser(user); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if err := store.CreateSession(&model.UserSession{
		ID: "regular-session", UserID: user.ID, ExpiresAt: time.Now().Add(time.Hour),
		AuthMethod: model.SessionAuthMethodPassword, AuthenticatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	runtime, err := NewOIDCRuntime()
	if err != nil {
		t.Fatalf("NewOIDCRuntime() error = %v", err)
	}
	handler := NewOIDCAdminHandler(runtime, newAuthHandlerWithRuntime(runtime))
	router := gin.New()
	group := router.Group("/api/admin/oidc")
	group.Use(middleware.SessionRequired(), middleware.AdminRequired())
	group.GET("", handler.Get)

	unauthenticated := httptest.NewRecorder()
	router.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/admin/oidc", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthenticated.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/oidc", nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "regular-session"})
	forbidden := httptest.NewRecorder()
	router.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("regular user status = %d", forbidden.Code)
	}
}
