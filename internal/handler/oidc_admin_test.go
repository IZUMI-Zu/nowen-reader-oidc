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
	request.Header.Set("X-Request-ID", "super-secret-value")
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "admin-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "super-secret-value") || strings.Contains(response.Body.String(), "secretCiphertext") {
		t.Fatalf("PUT response exposed secret material: %s", response.Body.String())
	}
	serverRequestID := response.Header().Get("X-Request-ID")
	if serverRequestID == "" || serverRequestID == "super-secret-value" {
		t.Fatalf("audit request ID was not server generated: %q", serverRequestID)
	}
	var auditRequestID string
	if err := store.DB().QueryRow(`SELECT "requestID" FROM "OIDCConfigAudit" WHERE "result" = 'success' ORDER BY "createdAt" DESC LIMIT 1`).Scan(&auditRequestID); err != nil {
		t.Fatalf("load success audit request ID: %v", err)
	}
	if auditRequestID != serverRequestID || strings.Contains(auditRequestID, "super-secret-value") {
		t.Fatalf("stored audit request ID = %q, response request ID = %q", auditRequestID, serverRequestID)
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

	clearBody := `{
		"expectedRevision":1,
		"clearClientSecret":true,
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
	clearRequest := httptest.NewRequest(http.MethodPut, "/reader/api/admin/oidc", strings.NewReader(clearBody))
	clearRequest.Header.Set("Content-Type", "application/json")
	clearRequest.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "admin-session"})
	clearResponse := httptest.NewRecorder()
	router.ServeHTTP(clearResponse, clearRequest)
	if clearResponse.Code != http.StatusOK {
		t.Fatalf("clear secret status = %d: %s", clearResponse.Code, clearResponse.Body.String())
	}
	var cleared oidcruntime.AdminConfig
	if err := json.Unmarshal(clearResponse.Body.Bytes(), &cleared); err != nil {
		t.Fatalf("decode clear-secret response: %v", err)
	}
	stored, err := (store.OIDCConfigStore{}).Load(context.Background())
	if err != nil {
		t.Fatalf("load cleared configuration: %v", err)
	}
	if cleared.ClientSecretConfigured || stored.SecretCiphertext != "" || stored.SecretKeyID != "" {
		t.Fatalf("clear-secret request retained credentials: response=%+v stored=%+v", cleared, stored)
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

	// Browsers send these content types cross-origin without a preflight, so the
	// admin API only accepts JSON. A charset parameter must still be accepted.
	// A stale revision proves the request got past the content-type gate: it is
	// rejected later, by the optimistic-concurrency check, with a different status.
	validBody := `{"expectedRevision":999,"config":{"enabled":false}}`
	for contentType, wantStatus := range map[string]int{
		"application/x-www-form-urlencoded": http.StatusBadRequest,
		"text/plain;charset=UTF-8":          http.StatusBadRequest,
		"multipart/form-data; boundary=x":   http.StatusBadRequest,
		"":                                  http.StatusBadRequest,
		"application/json; charset=utf-8":   http.StatusConflict,
	} {
		t.Run("content type "+contentType, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "/reader/api/admin/oidc", strings.NewReader(validBody))
			if contentType != "" {
				request.Header.Set("Content-Type", contentType)
			}
			request.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "admin-session"})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != wantStatus {
				t.Fatalf("content type %q status = %d, want %d: %s", contentType, response.Code, wantStatus, response.Body.String())
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
	// 4 malformed bodies + 4 rejected content types + the oversized body.
	if rejectedAuditCount != 9 {
		t.Fatalf("rejected-request audit count = %d, want 9", rejectedAuditCount)
	}
}

func TestOIDCAdminAPIRequiresAdministratorBrowserSession(t *testing.T) {
	t.Setenv("OIDC_CONFIG_MODE", "database")
	t.Setenv("DATA_DIR", t.TempDir())
	router := setupTestRouter(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	adminCookie := registerAndLogin(t, router)
	admin, err := store.GetUserByUsername("admin")
	if err != nil || admin == nil {
		t.Fatalf("load administrator: %+v, %v", admin, err)
	}
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

	_, apiKey, err := store.CreateAPIKey(admin.ID, "OIDC admin boundary", nil)
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	apiKeyResponse := performCredentialRequest(router, http.MethodGet, "/api/admin/oidc", nil, "", apiKey)
	if apiKeyResponse.Code != http.StatusUnauthorized {
		t.Fatalf("API key status = %d, want 401: %s", apiKeyResponse.Code, apiKeyResponse.Body.String())
	}

	if err := store.CreateSession(&model.UserSession{
		ID: "stale-admin-session", UserID: admin.ID, ExpiresAt: time.Now().Add(time.Hour),
		AuthMethod: model.SessionAuthMethodPassword, AuthenticatedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("CreateSession(stale admin) error = %v", err)
	}
	for _, request := range []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodPut, path: "/api/admin/oidc", body: map[string]any{}},
		{method: http.MethodPost, path: "/api/admin/oidc/probe", body: map[string]any{}},
		{method: http.MethodPost, path: "/api/admin/oidc/test-login"},
	} {
		response := performAuthedRequest(router, request.method, request.path, request.body, "stale-admin-session")
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "reauth_required") {
			t.Fatalf("stale session %s %s = %d: %s", request.method, request.path, response.Code, response.Body.String())
		}
	}

	for attempt := 1; attempt <= 21; attempt++ {
		response := performAuthedRequest(router, http.MethodPost, "/api/admin/oidc/probe", map[string]any{}, adminCookie)
		if attempt <= 20 && response.Code == http.StatusTooManyRequests {
			t.Fatalf("strict limiter rejected request %d too early", attempt)
		}
		if attempt == 21 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("strict limiter request 21 = %d, want 429: %s", response.Code, response.Body.String())
		}
	}
}

// Emergency recovery sets OIDC_FORCE_PASSWORD_LOGIN=true, which switches password
// login back on without touching OIDC_DISABLE_PASSWORD_LOGIN. The admin page has to
// keep showing the configured value, otherwise the operator cannot see which
// environment variable still has to be changed to make the recovery permanent.
func TestEnvironmentManagedAdminConfigReportsConfiguredPasswordLoginPolicy(t *testing.T) {
	t.Setenv("OIDC_CONFIG_MODE", "environment")
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_ISSUER_URL", "https://identity.example.com")
	t.Setenv("OIDC_CLIENT_ID", "nowen-reader")
	t.Setenv("OIDC_CLIENT_SECRET", "super-secret-value")
	t.Setenv("PUBLIC_URL", "https://reader.example.com")
	t.Setenv("OIDC_DISABLE_PASSWORD_LOGIN", "true")
	t.Setenv("OIDC_FORCE_PASSWORD_LOGIN", "true")
	t.Setenv("BASE_PATH", "/reader")
	t.Setenv("DATA_DIR", t.TempDir())
	if err := store.InitDB(filepath.Join(t.TempDir(), "oidc-env-admin.db")); err != nil {
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
	adminHandler := NewOIDCAdminHandler(runtime, newAuthHandlerWithRuntime(runtime))
	router := gin.New()
	group := router.Group("/reader/api/admin/oidc")
	group.Use(middleware.SessionRequired(), middleware.AdminRequired())
	group.GET("", adminHandler.Get)

	request := httptest.NewRequest(http.MethodGet, "/reader/api/admin/oidc", nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "admin-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", response.Code, response.Body.String())
	}
	var fetched oidcruntime.AdminConfig
	if err := json.Unmarshal(response.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("decode admin config: %v", err)
	}
	if fetched.ManagedBy != oidcruntime.ConfigSourceEnvironment || fetched.Editable {
		t.Fatalf("environment-managed config lost its read-only source: %+v", fetched)
	}
	if !fetched.ForcePasswordLogin {
		t.Fatalf("recovery override was not reported to the admin page: %+v", fetched)
	}
	if !fetched.Config.DisablePasswordLogin {
		t.Fatalf("environment-managed page hid OIDC_DISABLE_PASSWORD_LOGIN=true while recovery was active: %+v", fetched.Config)
	}
	if runtime.State().Config.DisablePasswordLogin {
		t.Fatalf("recovery override stopped taking effect on the login path: %+v", runtime.State())
	}
}
