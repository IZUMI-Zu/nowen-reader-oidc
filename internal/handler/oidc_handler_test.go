package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/auth/oidcruntime"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/middleware"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type fakeOIDCService struct {
	beginResult    oidcauth.AuthorizationRedirect
	beginError     error
	completeResult oidcauth.AuthenticatedIdentity
	completeError  error
	beginRequest   oidcauth.BeginRequest
	callback       oidcauth.CallbackRequest
	cancelRequest  oidcauth.CancelRequest
	cancelReturnTo string
	cancelError    error
}

type fakeHandlerOIDCRuntime struct {
	*fakeOIDCService
	state            oidcruntime.State
	verifiedUser     string
	verifiedIdentity oidcauth.AuthenticatedIdentity
	failureAudits    []string
}

func (r *fakeHandlerOIDCRuntime) RecordAdminFailure(_ context.Context, actorUserID, action, requestID, code string) error {
	r.failureAudits = append(r.failureAudits, actorUserID+"|"+action+"|"+requestID+"|"+code)
	return nil
}

func (r *fakeHandlerOIDCRuntime) State() oidcruntime.State { return r.state }

func (r *fakeHandlerOIDCRuntime) CompleteAndFinalize(ctx context.Context, request oidcauth.CallbackRequest, finalize oidcruntime.CompletionFinalizer) (oidcauth.AuthenticatedIdentity, error) {
	identity, err := r.Complete(ctx, request)
	if err != nil || identity.Purpose == oidcauth.PurposeConfigTest {
		return identity, err
	}
	if finalize == nil {
		return identity, errors.New("OIDC completion finalizer is required")
	}
	if err := finalize(r.state, identity); err != nil {
		return identity, err
	}
	return identity, nil
}

func (r *fakeHandlerOIDCRuntime) CompleteConfigTest(_ context.Context, actorUserID, _ string, identity oidcauth.AuthenticatedIdentity) (oidcruntime.AdminConfig, error) {
	r.verifiedUser = actorUserID
	r.verifiedIdentity = identity
	return oidcruntime.AdminConfig{Status: "ready"}, nil
}

func (s *fakeOIDCService) Begin(_ context.Context, request oidcauth.BeginRequest) (oidcauth.AuthorizationRedirect, error) {
	s.beginRequest = request
	return s.beginResult, s.beginError
}

func (s *fakeOIDCService) Complete(_ context.Context, request oidcauth.CallbackRequest) (oidcauth.AuthenticatedIdentity, error) {
	s.callback = request
	return s.completeResult, s.completeError
}

func (s *fakeOIDCService) Cancel(_ context.Context, request oidcauth.CancelRequest) (string, error) {
	s.cancelRequest = request
	return s.cancelReturnTo, s.cancelError
}

func setupOIDCHandlerTest(t *testing.T, cfg config.OIDCConfig, service *fakeOIDCService) *gin.Engine {
	t.Helper()
	t.Setenv("BASE_PATH", "/reader")
	if err := store.InitDB(filepath.Join(t.TempDir(), "handler-oidc.db")); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	if err := store.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	t.Cleanup(store.CloseDB)

	r := gin.New()
	h := newAuthHandlerWithOIDC(cfg, service, nil)
	r.GET("/reader/api/auth/oidc/login", h.OIDCLogin)
	r.GET("/reader/api/auth/oidc/callback", h.OIDCCallback)
	r.POST("/reader/api/auth/oidc/link", middleware.SessionRequired(), middleware.RequireRecentAuthentication(middleware.RecentAuthenticationWindow), h.OIDCLink)
	r.GET("/reader/api/auth/oidc/reauth", middleware.SessionRequired(), h.OIDCReauth)
	r.DELETE("/reader/api/auth/oidc/link", middleware.SessionRequired(), middleware.RequireRecentAuthentication(middleware.RecentAuthenticationWindow), h.OIDCUnlink)
	r.POST("/reader/api/auth/reauth/password", middleware.SessionRequired(), h.PasswordReauth)
	r.POST("/reader/api/auth/password", middleware.SessionRequired(), middleware.RequireRecentAuthentication(middleware.RecentAuthenticationWindow), h.SetInitialPassword)
	r.POST("/reader/api/auth/register", h.Register)
	r.POST("/reader/api/auth/login", h.Login)
	r.GET("/reader/api/auth/me", h.Me)
	return r
}

func createLocalAdminForOIDCTest(t *testing.T) {
	t.Helper()
	if err := store.CreateUser(&model.User{
		ID: "local-admin", Username: "admin", Password: "hash", Nickname: "Admin", Role: "admin", AiEnabled: true,
	}); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
}

func enabledOIDCHandlerConfig() config.OIDCConfig {
	return config.OIDCConfig{
		Enabled: true, IssuerURL: "https://identity.example.com", ClientID: "client", ClientSecret: "secret",
		ProviderName: "Company Login", PublicURL: "https://reader.example.com",
		CallbackURL:   "https://reader.example.com/reader/api/auth/oidc/callback",
		AutoProvision: true, BootstrapAdminSubjects: map[string]struct{}{},
		SessionAbsoluteTTL: 8 * time.Hour, SecureCookies: true,
	}
}

func TestOIDCLoginRedirectSetsShortBrowserBindingCookie(t *testing.T) {
	service := &fakeOIDCService{beginResult: oidcauth.AuthorizationRedirect{
		URL: "https://identity.example.com/authorize?state=state", BindingToken: "browser-binding", ExpiresAt: time.Now().Add(5 * time.Minute),
	}}
	r := setupOIDCHandlerTest(t, enabledOIDCHandlerConfig(), service)

	request := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/login?returnTo=/reader/books", nil)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != http.StatusFound || response.Header().Get("Location") != service.beginResult.URL {
		t.Fatalf("login redirect = %d %q", response.Code, response.Header().Get("Location"))
	}
	if service.beginRequest.Purpose != oidcauth.PurposeLogin || service.beginRequest.ReturnTo != "/reader/books" {
		t.Fatalf("Begin request = %+v", service.beginRequest)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("binding cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != OIDCTransactionCookie || cookie.Value != "browser-binding" || cookie.Path != "/reader/api/auth/oidc" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe transaction cookie = %#v", cookie)
	}
}

func TestOIDCCallbackProvisionsSeparateLocalUserAndIssuesBoundedSession(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	service := &fakeOIDCService{completeResult: oidcauth.AuthenticatedIdentity{
		VerifiedIdentity: oidcauth.VerifiedIdentity{
			Issuer: cfg.IssuerURL, Subject: "subject-123", PreferredUsername: "admin",
			DisplayName: "OIDC Alice", Email: "admin@example.com", EmailVerified: true,
		},
		Purpose: oidcauth.PurposeLogin, ReturnTo: "/reader/books",
	}}
	r := setupOIDCHandlerTest(t, cfg, service)
	createLocalAdminForOIDCTest(t)

	request := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?code=valid-code&state=valid-state", nil)
	request.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "browser-binding"})
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/reader/books" {
		t.Fatalf("callback redirect = %d %q: %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if service.callback.Code != "valid-code" || service.callback.State != "valid-state" || service.callback.BindingToken != "browser-binding" {
		t.Fatalf("Complete request = %+v", service.callback)
	}

	var sessionCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == middleware.SessionCookie && cookie.Value != "" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure {
		t.Fatalf("secure OIDC session cookie not issued: %#v", response.Result().Cookies())
	}
	session, user, err := store.GetSessionWithUser(sessionCookie.Value)
	if err != nil || session == nil || user == nil {
		t.Fatalf("GetSessionWithUser() = %+v, %+v, %v", session, user, err)
	}
	if session.AuthMethod != "oidc" || session.AbsoluteExpiresAt == nil || time.Until(*session.AbsoluteExpiresAt) > cfg.SessionAbsoluteTTL+time.Minute {
		t.Fatalf("OIDC session is not absolutely bounded: %+v", session)
	}
	if user.ID == "local-admin" || user.Username == "admin" || user.Password != "" || user.Role != "user" {
		t.Fatalf("OIDC identity was merged into the local admin: %+v", user)
	}
}

func TestOIDCConfigurationTestBindsCurrentAdministratorAndMarksDraftVerified(t *testing.T) {
	t.Setenv("BASE_PATH", "/reader")
	if err := store.InitDB(filepath.Join(t.TempDir(), "handler-oidc-config-test.db")); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	if err := store.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	t.Cleanup(store.CloseDB)
	createLocalAdminForOIDCTest(t)
	if err := store.CreateSession(&model.UserSession{
		ID: "admin-config-session", UserID: "local-admin", ExpiresAt: time.Now().Add(time.Hour),
		AuthMethod: model.SessionAuthMethodPassword, AuthenticatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	cfg := enabledOIDCHandlerConfig()
	cfg.Enabled = false
	runtime := &fakeHandlerOIDCRuntime{
		fakeOIDCService: &fakeOIDCService{completeResult: oidcauth.AuthenticatedIdentity{
			VerifiedIdentity: oidcauth.VerifiedIdentity{
				Issuer: cfg.IssuerURL, Subject: "admin-oidc-subject", Nonce: "verified",
			},
			Purpose: oidcauth.PurposeConfigTest, SessionUserID: "local-admin", ReturnTo: "/reader/settings?tab=authentication",
		}},
		state: oidcruntime.State{Config: cfg, Source: oidcruntime.ConfigSourceDatabase, Available: true},
	}
	router := gin.New()
	router.GET("/reader/api/auth/oidc/callback", newAuthHandlerWithRuntime(runtime).OIDCCallback)
	request := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?code=code&state=state", nil)
	request.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "binding"})
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "admin-config-session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/reader/settings?tab=authentication" {
		t.Fatalf("callback = %d %q: %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if runtime.verifiedUser != "local-admin" {
		t.Fatalf("verified actor = %q", runtime.verifiedUser)
	}
	if runtime.verifiedIdentity.Subject != "admin-oidc-subject" || runtime.verifiedIdentity.Purpose != oidcauth.PurposeConfigTest {
		t.Fatalf("verified identity = %+v", runtime.verifiedIdentity)
	}
}

func TestOIDCConfigurationTestAuditsIdentityValidationFailure(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	runtime := &fakeHandlerOIDCRuntime{
		fakeOIDCService: &fakeOIDCService{
			completeResult: oidcauth.AuthenticatedIdentity{
				Purpose: oidcauth.PurposeConfigTest, SessionUserID: "local-admin", ReturnTo: "/reader/settings?tab=authentication",
			},
			completeError: oidcauth.ErrInvalidIdentity,
		},
		state: oidcruntime.State{Config: cfg, Source: oidcruntime.ConfigSourceDatabase, Available: true, Ready: true},
	}
	router := gin.New()
	router.GET("/reader/api/auth/oidc/callback", newAuthHandlerWithRuntime(runtime).OIDCCallback)
	request := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?code=code&state=state", nil)
	request.Header.Set("X-Request-ID", "request-invalid-auth-time")
	request.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "binding"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/reader/settings?oidc_error=identity_validation_failed&tab=authentication" {
		t.Fatalf("callback = %d %q: %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if !reflect.DeepEqual(runtime.failureAudits, []string{"local-admin|verify|request-invalid-auth-time|identity_validation_failed"}) {
		t.Fatalf("failure audits = %#v", runtime.failureAudits)
	}
}

func TestOIDCConfigurationTestAuditsSessionMismatchBeforeVerificationCommit(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	runtime := &fakeHandlerOIDCRuntime{
		fakeOIDCService: &fakeOIDCService{completeResult: oidcauth.AuthenticatedIdentity{
			VerifiedIdentity: oidcauth.VerifiedIdentity{Issuer: cfg.IssuerURL, Subject: "subject"},
			Purpose:          oidcauth.PurposeConfigTest, SessionUserID: "local-admin", ReturnTo: "/reader/settings?tab=authentication",
		}},
		state: oidcruntime.State{Config: cfg, Source: oidcruntime.ConfigSourceDatabase, Available: true, Ready: true},
	}
	router := gin.New()
	router.GET("/reader/api/auth/oidc/callback", newAuthHandlerWithRuntime(runtime).OIDCCallback)
	request := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?code=code&state=state", nil)
	request.Header.Set("X-Request-ID", "request-session-mismatch")
	request.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "binding"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/reader/settings?oidc_error=session_mismatch&tab=authentication" {
		t.Fatalf("callback = %d %q: %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if !reflect.DeepEqual(runtime.failureAudits, []string{"local-admin|verify|request-session-mismatch|session_mismatch"}) {
		t.Fatalf("failure audits = %#v", runtime.failureAudits)
	}
}

func TestOIDCCallbackRejectsOrdinaryLoginAfterOIDCIsDisabled(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	cfg.Enabled = false
	service := &fakeOIDCService{completeResult: oidcauth.AuthenticatedIdentity{
		VerifiedIdentity: oidcauth.VerifiedIdentity{Issuer: cfg.IssuerURL, Subject: "subject", Nonce: "verified"},
		Purpose:          oidcauth.PurposeLogin, ReturnTo: "/reader/books",
	}}
	router := setupOIDCHandlerTest(t, cfg, service)
	request := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?code=code&state=state", nil)
	request.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "binding"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/reader/books?oidc_error=configuration_disabled" {
		t.Fatalf("disabled callback = %d %q: %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func TestOIDCCallbackUnknownIdentityIsForbiddenWhenProvisioningDisabled(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	cfg.AutoProvision = false
	service := &fakeOIDCService{completeResult: oidcauth.AuthenticatedIdentity{
		VerifiedIdentity: oidcauth.VerifiedIdentity{Issuer: cfg.IssuerURL, Subject: "unknown", Nonce: "verified"},
		Purpose:          oidcauth.PurposeLogin, ReturnTo: "/reader/",
	}}
	r := setupOIDCHandlerTest(t, cfg, service)
	createLocalAdminForOIDCTest(t)

	request := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?code=code&state=state", nil)
	request.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "binding"})
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/reader/?oidc_error=account_not_authorized" {
		t.Fatalf("unknown identity callback = %d %q: %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func TestOIDCCallbackCancellationConsumesTransactionAndReturnsToLocalPage(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	service := &fakeOIDCService{cancelReturnTo: "/reader/settings?tab=account"}
	r := setupOIDCHandlerTest(t, cfg, service)
	request := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?error=access_denied&state=valid-state", nil)
	request.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "browser-binding"})
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/reader/settings?oidc_error=access_denied&tab=account" {
		t.Fatalf("cancel callback = %d %q: %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if service.cancelRequest.State != "valid-state" || service.cancelRequest.BindingToken != "browser-binding" || service.callback.Code != "" {
		t.Fatalf("cancel/complete requests = %+v / %+v", service.cancelRequest, service.callback)
	}
}

func TestOIDCCallbackUsesStableLocalErrorCodes(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	service := &fakeOIDCService{
		completeResult: oidcauth.AuthenticatedIdentity{ReturnTo: "/reader/settings?tab=account"},
		completeError:  oidcauth.ErrInvalidIdentity,
	}
	r := setupOIDCHandlerTest(t, cfg, service)
	request := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?code=secret-code&state=secret-state&error_description=provider-secret", nil)
	request.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "browser-binding"})
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/reader/settings?oidc_error=identity_validation_failed&tab=account" {
		t.Fatalf("invalid identity callback = %d %q: %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if strings.Contains(response.Header().Get("Location"), "provider-secret") || strings.Contains(response.Header().Get("Location"), "secret-code") {
		t.Fatalf("callback leaked provider parameters in redirect: %q", response.Header().Get("Location"))
	}
}

func TestOIDCCallbackReturns503WhenProviderIsUnavailable(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	service := &fakeOIDCService{
		completeResult: oidcauth.AuthenticatedIdentity{ReturnTo: "/reader/books"},
		completeError:  oidcauth.ErrProviderUnavailable,
	}
	r := setupOIDCHandlerTest(t, cfg, service)
	request := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?code=code&state=state", nil)
	request.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "binding"})
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "provider is unavailable") {
		t.Fatalf("provider outage callback = %d: %s", response.Code, response.Body.String())
	}
}

func TestMeAdvertisesOIDCWithoutExposingConfiguration(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	r := setupOIDCHandlerTest(t, cfg, &fakeOIDCService{})
	createLocalAdminForOIDCTest(t)

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/reader/api/auth/me", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("Me() status = %d", response.Code)
	}
	var body struct {
		LoginMethods struct {
			Password bool `json:"password"`
			OIDC     struct {
				Enabled     bool   `json:"enabled"`
				DisplayName string `json:"displayName"`
			} `json:"oidc"`
		} `json:"loginMethods"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	if !body.LoginMethods.Password || !body.LoginMethods.OIDC.Enabled || body.LoginMethods.OIDC.DisplayName != "Company Login" {
		t.Fatalf("OIDC status = %+v", body.LoginMethods.OIDC)
	}
	if containsAny(response.Body.String(), cfg.ClientSecret, cfg.ClientID, cfg.IssuerURL) {
		t.Fatalf("/me exposed OIDC configuration: %s", response.Body.String())
	}
}

func TestOIDCPasswordLoginSwitchDisablesLoginRegistrationAndSetup(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	cfg.DisablePasswordLogin = true
	r := setupOIDCHandlerTest(t, cfg, &fakeOIDCService{})

	register := performRequest(r, http.MethodPost, "/reader/api/auth/register", map[string]string{
		"username": "attacker", "password": "password123",
	})
	if register.Code != http.StatusForbidden || !strings.Contains(register.Body.String(), "password_login_disabled") {
		t.Fatalf("disabled registration = %d: %s", register.Code, register.Body.String())
	}
	if count, err := store.CountUsers(); err != nil || count != 0 {
		t.Fatalf("disabled registration created users: count=%d err=%v", count, err)
	}

	login := performRequest(r, http.MethodPost, "/reader/api/auth/login", map[string]string{
		"username": "admin", "password": "password123",
	})
	if login.Code != http.StatusForbidden || !strings.Contains(login.Body.String(), "password_login_disabled") {
		t.Fatalf("disabled login = %d: %s", login.Code, login.Body.String())
	}

	me := httptest.NewRecorder()
	r.ServeHTTP(me, httptest.NewRequest(http.MethodGet, "/reader/api/auth/me", nil))
	var body struct {
		NeedsSetup       bool   `json:"needsSetup"`
		RegistrationMode string `json:"registrationMode"`
		LoginMethods     struct {
			Password bool `json:"password"`
			OIDC     struct {
				Enabled bool `json:"enabled"`
			} `json:"oidc"`
		} `json:"loginMethods"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	if me.Code != http.StatusOK || body.NeedsSetup || body.RegistrationMode != "closed" || body.LoginMethods.Password || !body.LoginMethods.OIDC.Enabled {
		t.Fatalf("disabled password capability /me = %d %+v", me.Code, body)
	}
}

func TestMeAllowsFirstOIDCLoginOnlyWithSafeBootstrapPolicy(t *testing.T) {
	for _, tt := range []struct {
		name              string
		autoProvision     bool
		bootstrapSubjects map[string]struct{}
		wantNeedsSetup    bool
	}{
		{name: "no subject allowlist", autoProvision: true, bootstrapSubjects: map[string]struct{}{}, wantNeedsSetup: true},
		{name: "provisioning disabled", bootstrapSubjects: map[string]struct{}{"admin-sub": {}}, wantNeedsSetup: true},
		{name: "safe OIDC bootstrap", autoProvision: true, bootstrapSubjects: map[string]struct{}{"admin-sub": {}}, wantNeedsSetup: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := enabledOIDCHandlerConfig()
			cfg.AutoProvision = tt.autoProvision
			cfg.BootstrapAdminSubjects = tt.bootstrapSubjects
			r := setupOIDCHandlerTest(t, cfg, &fakeOIDCService{})
			response := httptest.NewRecorder()
			r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/reader/api/auth/me", nil))
			var body struct {
				NeedsSetup   bool `json:"needsSetup"`
				LoginMethods struct {
					OIDC struct {
						Enabled bool `json:"enabled"`
					} `json:"oidc"`
				} `json:"loginMethods"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode /me: %v", err)
			}
			if body.NeedsSetup != tt.wantNeedsSetup || !body.LoginMethods.OIDC.Enabled {
				t.Fatalf("/me = %+v, want needsSetup=%v", body, tt.wantNeedsSetup)
			}
		})
	}
}

func TestSafeOIDCBootstrapBlocksLocalFirstAdminRegistration(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	cfg.AutoProvision = true
	cfg.BootstrapAdminSubjects = map[string]struct{}{"admin-sub": {}}
	r := setupOIDCHandlerTest(t, cfg, &fakeOIDCService{})
	response := performRequest(r, http.MethodPost, "/reader/api/auth/register", map[string]string{
		"username": "attacker", "password": "password123",
	})
	if response.Code != http.StatusForbidden {
		t.Fatalf("local bootstrap registration status = %d, want 403: %s", response.Code, response.Body.String())
	}
	if count, err := store.CountUsers(); err != nil || count != 0 {
		t.Fatalf("CountUsers() = %d, %v; local registration claimed bootstrap", count, err)
	}
}

func containsAny(value string, secrets ...string) bool {
	for _, secret := range secrets {
		if secret != "" && strings.Contains(value, secret) {
			return true
		}
	}
	return false
}

func createSessionForOIDCTest(t *testing.T, id, userID, method string, authenticatedAt time.Time) string {
	t.Helper()
	if err := store.CreateSession(&model.UserSession{
		ID: id, UserID: userID, ExpiresAt: time.Now().Add(time.Hour), AuthMethod: method, AuthenticatedAt: authenticatedAt,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	return id
}

func TestOIDCLinkBindsVerifiedIdentityOnlyToTheInitiatingSessionUser(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	service := &fakeOIDCService{beginResult: oidcauth.AuthorizationRedirect{
		URL: "https://identity.example.com/authorize", BindingToken: "binding", ExpiresAt: time.Now().Add(5 * time.Minute),
	}}
	r := setupOIDCHandlerTest(t, cfg, service)
	createLocalAdminForOIDCTest(t)
	sessionID := createSessionForOIDCTest(t, "local-session", "local-admin", "password", time.Now())

	begin := httptest.NewRequest(http.MethodPost, "/reader/api/auth/oidc/link?returnTo=/reader/settings", nil)
	begin.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: sessionID})
	beginResponse := httptest.NewRecorder()
	r.ServeHTTP(beginResponse, begin)
	if beginResponse.Code != http.StatusFound || service.beginRequest.Purpose != oidcauth.PurposeLink || service.beginRequest.SessionUserID != "local-admin" {
		t.Fatalf("link begin = %d, %+v", beginResponse.Code, service.beginRequest)
	}

	service.completeResult = oidcauth.AuthenticatedIdentity{
		VerifiedIdentity: oidcauth.VerifiedIdentity{Issuer: cfg.IssuerURL, Subject: "linked-subject"},
		Purpose:          oidcauth.PurposeLink, SessionUserID: "local-admin", ReturnTo: "/reader/settings",
	}
	callback := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?code=code&state=state", nil)
	callback.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: sessionID})
	callback.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "binding"})
	callbackResponse := httptest.NewRecorder()
	r.ServeHTTP(callbackResponse, callback)
	if callbackResponse.Code != http.StatusSeeOther || callbackResponse.Header().Get("Location") != "/reader/settings" {
		t.Fatalf("link callback = %d %q: %s", callbackResponse.Code, callbackResponse.Header().Get("Location"), callbackResponse.Body.String())
	}
	linked, err := store.OIDCIdentityBelongsToUser(context.Background(), "local-admin", cfg.IssuerURL, "linked-subject")
	if err != nil || !linked {
		t.Fatalf("linked identity = %v, %v", linked, err)
	}
}

func TestOIDCReauthRequiresTheSameLinkedIdentityAndRefreshesAuthenticationTime(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	service := &fakeOIDCService{beginResult: oidcauth.AuthorizationRedirect{
		URL: "https://identity.example.com/authorize", BindingToken: "binding", ExpiresAt: time.Now().Add(5 * time.Minute),
	}}
	r := setupOIDCHandlerTest(t, cfg, service)
	createLocalAdminForOIDCTest(t)
	identity := oidcauth.VerifiedIdentity{Issuer: cfg.IssuerURL, Subject: "admin-subject"}
	if err := store.LinkOIDCIdentity(context.Background(), "local-admin", identity); err != nil {
		t.Fatalf("LinkOIDCIdentity() error = %v", err)
	}
	oldAuthTime := time.Now().Add(-time.Hour)
	sessionID := createSessionForOIDCTest(t, "oidc-session", "local-admin", "oidc", oldAuthTime)

	begin := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/reauth?returnTo=/reader/settings", nil)
	begin.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: sessionID})
	beginResponse := httptest.NewRecorder()
	r.ServeHTTP(beginResponse, begin)
	if beginResponse.Code != http.StatusFound || service.beginRequest.Purpose != oidcauth.PurposeReauth || service.beginRequest.SessionUserID != "local-admin" {
		t.Fatalf("reauth begin = %d, %+v", beginResponse.Code, service.beginRequest)
	}

	service.completeResult = oidcauth.AuthenticatedIdentity{
		VerifiedIdentity: identity, Purpose: oidcauth.PurposeReauth,
		SessionUserID: "local-admin", ReturnTo: "/reader/settings",
	}
	callback := httptest.NewRequest(http.MethodGet, "/reader/api/auth/oidc/callback?code=code&state=state", nil)
	callback.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: sessionID})
	callback.AddCookie(&http.Cookie{Name: OIDCTransactionCookie, Value: "binding"})
	response := httptest.NewRecorder()
	r.ServeHTTP(response, callback)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("reauth callback = %d: %s", response.Code, response.Body.String())
	}
	session, _, err := store.GetSessionWithUser(sessionID)
	if err != nil || session == nil || time.Since(session.AuthenticatedAt) > time.Minute {
		t.Fatalf("session authentication was not refreshed: %+v, %v", session, err)
	}
}

func TestPasswordReauthRefreshesOnlyAfterCorrectLocalPassword(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	r := setupOIDCHandlerTest(t, cfg, &fakeOIDCService{})
	createLocalAdminForOIDCTest(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword() error = %v", err)
	}
	if err := store.UpdateUserPassword("local-admin", string(hash)); err != nil {
		t.Fatalf("UpdateUserPassword() error = %v", err)
	}
	oldAuthTime := time.Now().Add(-time.Hour)
	sessionID := createSessionForOIDCTest(t, "password-session", "local-admin", "password", oldAuthTime)

	wrong := performAuthedRequest(r, http.MethodPost, "/reader/api/auth/reauth/password", map[string]string{"password": "wrong"}, sessionID)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", wrong.Code)
	}
	session, _, err := store.GetSessionWithUser(sessionID)
	if err != nil || session == nil || session.AuthenticatedAt.After(oldAuthTime.Add(time.Minute)) {
		t.Fatalf("wrong password refreshed session: %+v, %v", session, err)
	}

	correct := performAuthedRequest(r, http.MethodPost, "/reader/api/auth/reauth/password", map[string]string{"password": "password123"}, sessionID)
	if correct.Code != http.StatusNoContent {
		t.Fatalf("correct password status = %d, want 204: %s", correct.Code, correct.Body.String())
	}
	session, _, err = store.GetSessionWithUser(sessionID)
	if err != nil || session == nil || time.Since(session.AuthenticatedAt) > time.Minute {
		t.Fatalf("correct password did not refresh session: %+v, %v", session, err)
	}
}

func TestOIDCOnlyUserCanSetInitialPasswordOnlyAfterRecentAuthentication(t *testing.T) {
	cfg := enabledOIDCHandlerConfig()
	r := setupOIDCHandlerTest(t, cfg, &fakeOIDCService{})
	if err := store.CreateUser(&model.User{
		ID: "oidc-only", Username: "reader", Password: "", Nickname: "Reader", Role: "user",
	}); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	staleSession := createSessionForOIDCTest(t, "stale-oidc-session", "oidc-only", model.SessionAuthMethodOIDC, time.Now().Add(-time.Hour))
	stale := performAuthedRequest(r, http.MethodPost, "/reader/api/auth/password", map[string]string{"newPassword": "breakglass123"}, staleSession)
	if stale.Code != http.StatusUnauthorized || !strings.Contains(stale.Body.String(), "reauth_required") {
		t.Fatalf("stale initial password status = %d: %s", stale.Code, stale.Body.String())
	}

	freshSession := createSessionForOIDCTest(t, "fresh-oidc-session", "oidc-only", model.SessionAuthMethodOIDC, time.Now())
	response := performAuthedRequest(r, http.MethodPost, "/reader/api/auth/password", map[string]string{"newPassword": "breakglass123"}, freshSession)
	if response.Code != http.StatusNoContent {
		t.Fatalf("initial password status = %d: %s", response.Code, response.Body.String())
	}
	user, err := store.GetUserByID("oidc-only")
	if err != nil || user == nil || bcrypt.CompareHashAndPassword([]byte(user.Password), []byte("breakglass123")) != nil {
		t.Fatalf("initial password was not stored safely: %+v, %v", user, err)
	}
	second := performAuthedRequest(r, http.MethodPost, "/reader/api/auth/password", map[string]string{"newPassword": "replacement123"}, freshSession)
	if second.Code != http.StatusConflict {
		t.Fatalf("second initial-password attempt status = %d, want 409", second.Code)
	}
}
