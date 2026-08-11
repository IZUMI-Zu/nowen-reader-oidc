package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/auth/oidcruntime"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/middleware"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

const OIDCTransactionCookie = "nowen_oidc_tx"

type staticOIDCRuntime struct {
	state   oidcruntime.State
	service oidcLoginService
}

func newAuthHandlerWithOIDC(cfg config.OIDCConfig, service oidcLoginService, err error) *AuthHandler {
	state := oidcruntime.State{
		Config: cfg, Source: oidcruntime.ConfigSourceEnvironment,
		Available: service != nil && err == nil, Ready: cfg.Enabled && service != nil && err == nil,
		PasswordLoginPolicyDisabled: cfg.DisablePasswordLogin,
	}
	if err != nil {
		state.ErrorCode = "configuration_invalid"
	}
	return newAuthHandlerWithRuntime(&staticOIDCRuntime{state: state, service: service})
}

func newAuthHandlerWithRuntime(runtime oidcRuntime) *AuthHandler {
	return &AuthHandler{oidc: runtime}
}

func (r *staticOIDCRuntime) State() oidcruntime.State { return r.state }

func (r *staticOIDCRuntime) WithStateLease(run oidcruntime.StateLease) error {
	return run(r.State())
}

func (r *staticOIDCRuntime) Begin(ctx context.Context, request oidcauth.BeginRequest) (oidcauth.AuthorizationRedirect, error) {
	if r.service == nil {
		return oidcauth.AuthorizationRedirect{}, oidcauth.ErrProviderUnavailable
	}
	result, err := r.service.Begin(ctx, request)
	result.CookieSecure = r.state.Config.SecureCookies
	return result, err
}

func (r *staticOIDCRuntime) Complete(ctx context.Context, request oidcauth.CallbackRequest) (oidcauth.AuthenticatedIdentity, error) {
	if r.service == nil {
		return oidcauth.AuthenticatedIdentity{}, oidcauth.ErrProviderUnavailable
	}
	return r.service.Complete(ctx, request)
}

func (r *staticOIDCRuntime) CompleteAndFinalize(ctx context.Context, request oidcauth.CallbackRequest, finalize oidcruntime.CompletionFinalizer) (oidcauth.AuthenticatedIdentity, error) {
	identity, err := r.Complete(ctx, request)
	if err != nil || identity.Purpose == oidcauth.PurposeConfigTest {
		return identity, err
	}
	if finalize == nil {
		return identity, errors.New("OIDC completion finalizer is required")
	}
	if err := finalize(r.State(), identity); err != nil {
		return identity, err
	}
	return identity, nil
}

func (r *staticOIDCRuntime) Cancel(ctx context.Context, request oidcauth.CancelRequest) (string, error) {
	if r.service == nil {
		return "", oidcauth.ErrProviderUnavailable
	}
	return r.service.Cancel(ctx, request)
}

func (r *staticOIDCRuntime) CompleteConfigTest(context.Context, string, string, string, oidcauth.AuthenticatedIdentity) (oidcruntime.AdminConfig, error) {
	return oidcruntime.AdminConfig{}, oidcruntime.ErrEnvironmentManaged
}

func (r *staticOIDCRuntime) RecordAdminFailure(context.Context, string, string, string, string) error {
	return nil
}

func oidcStatus(state oidcruntime.State) gin.H {
	return gin.H{
		"enabled":     state.Config.Enabled && state.Ready,
		"displayName": state.Config.ProviderName,
	}
}

func oidcBootstrapReady(state oidcruntime.State) bool {
	return state.Config.Enabled && state.Ready && state.Config.AutoProvision &&
		len(state.Config.BootstrapAdminSubjects) > 0
}

// OIDCLogin starts a backend-managed Authorization Code + PKCE flow.
func (h *AuthHandler) OIDCLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	state := h.oidc.State()
	if !state.Config.Enabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "OIDC login is not enabled"})
		return
	}
	if !state.Ready {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC login is temporarily unavailable"})
		return
	}
	result, err := h.oidc.Begin(c.Request.Context(), oidcauth.BeginRequest{
		Purpose: oidcauth.PurposeLogin, ReturnTo: c.Query("returnTo"),
	})
	if err != nil {
		if errors.Is(err, oidcauth.ErrInvalidReturnTo) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid return target"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC login is temporarily unavailable"})
		return
	}
	h.setOIDCTransactionCookie(c, result.BindingToken, result.ExpiresAt, result.CookieSecure)
	c.Redirect(http.StatusFound, result.URL)
}

// OIDCLink starts an explicit identity-binding transaction for the current
// local session. The route is protected by recent-authentication middleware.
func (h *AuthHandler) OIDCLink(c *gin.Context) {
	h.beginOIDCForCurrentUser(c, oidcauth.PurposeLink)
}

// OIDCReauth starts an OIDC transaction that can only refresh the current
// session when the same already-linked external identity returns.
func (h *AuthHandler) OIDCReauth(c *gin.Context) {
	h.beginOIDCForCurrentUser(c, oidcauth.PurposeReauth)
}

func (h *AuthHandler) beginOIDCForCurrentUser(c *gin.Context, purpose oidcauth.Purpose) {
	c.Header("Cache-Control", "no-store")
	state := h.oidc.State()
	if !state.Config.Enabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "OIDC login is not enabled"})
		return
	}
	if !state.Ready {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC login is temporarily unavailable"})
		return
	}
	user := middleware.GetCurrentUser(c)
	credential := middleware.GetCurrentCredential(c)
	if user == nil || credential == nil || credential.Type != middleware.CredentialSession {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Browser session required"})
		return
	}
	result, err := h.oidc.Begin(c.Request.Context(), oidcauth.BeginRequest{
		Purpose: purpose, SessionUserID: user.ID, ReturnTo: c.Query("returnTo"),
	})
	if err != nil {
		if errors.Is(err, oidcauth.ErrInvalidReturnTo) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid return target"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC login is temporarily unavailable"})
		return
	}
	h.setOIDCTransactionCookie(c, result.BindingToken, result.ExpiresAt, result.CookieSecure)
	c.Redirect(http.StatusFound, result.URL)
}

// OIDCCallback completes the one-time authorization transaction and converts
// the verified external identity into a bounded local session.
func (h *AuthHandler) OIDCCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	state := h.oidc.State()
	if !state.Available {
		if !state.Config.Enabled {
			c.JSON(http.StatusNotFound, gin.H{"error": "OIDC login is not enabled"})
		} else {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC login is temporarily unavailable"})
		}
		return
	}
	binding, err := c.Cookie(OIDCTransactionCookie)
	if err != nil || binding == "" || c.Query("state") == "" {
		h.clearOIDCTransactionCookie(c)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid OIDC callback"})
		return
	}
	h.clearOIDCTransactionCookie(c)
	if c.Query("error") != "" {
		returnTo, cancelErr := h.oidc.Cancel(c.Request.Context(), oidcauth.CancelRequest{
			State: c.Query("state"), BindingToken: binding,
		})
		if cancelErr != nil {
			if errors.Is(cancelErr, oidcauth.ErrConfigurationChanged) {
				redirectOIDCFailure(c, returnTo, "oidc_configuration_changed")
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "OIDC login transaction is invalid or expired"})
			return
		}
		c.Redirect(http.StatusSeeOther, oidcErrorReturnTo(returnTo, "access_denied"))
		return
	}
	if c.Query("code") == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid OIDC callback"})
		return
	}
	identity, err := h.oidc.CompleteAndFinalize(c.Request.Context(), oidcauth.CallbackRequest{
		State: c.Query("state"), Code: c.Query("code"), BindingToken: binding,
	}, func(state oidcruntime.State, identity oidcauth.AuthenticatedIdentity) error {
		if code := h.finalizeOIDCCallback(c, state, identity); code != "" {
			return &oidcCallbackFinalizationError{code: code}
		}
		return nil
	})
	if err != nil {
		if identity.Purpose == oidcauth.PurposeConfigTest {
			h.recordOIDCConfigTestFailure(c, identity.SessionUserID, callbackFailureAuditCode(err))
		}
		var finalizationError *oidcCallbackFinalizationError
		switch {
		case errors.As(err, &finalizationError):
			redirectOIDCFailure(c, identity.ReturnTo, finalizationError.code)
		case errors.Is(err, oidcauth.ErrInvalidTransaction):
			c.JSON(http.StatusBadRequest, gin.H{"error": "OIDC login transaction is invalid or expired"})
		case errors.Is(err, oidcauth.ErrInvalidIdentity):
			redirectOIDCFailure(c, identity.ReturnTo, "identity_validation_failed")
		case errors.Is(err, oidcauth.ErrProviderUnavailable):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC provider is unavailable"})
		case errors.Is(err, oidcauth.ErrConfigurationChanged):
			redirectOIDCFailure(c, identity.ReturnTo, "oidc_configuration_changed")
		default:
			redirectOIDCFailure(c, identity.ReturnTo, "login_failed")
		}
		return
	}
	if identity.Purpose == oidcauth.PurposeConfigTest {
		user, credential, ok := currentTransactionSession(c, identity.SessionUserID, identity.SessionID)
		if !ok || user.Role != "admin" {
			h.recordOIDCConfigTestFailure(c, identity.SessionUserID, "session_mismatch")
			redirectOIDCFailure(c, identity.ReturnTo, "session_mismatch")
			return
		}
		if _, err := h.oidc.CompleteConfigTest(c.Request.Context(), user.ID, credential.ID, oidcAuditRequestID(c), identity); err != nil {
			if errors.Is(err, oidcauth.ErrConfigurationChanged) || errors.Is(err, oidcruntime.ErrConfigConflict) {
				redirectOIDCFailure(c, identity.ReturnTo, "oidc_configuration_changed")
				return
			}
			redirectOIDCFailure(c, identity.ReturnTo, "configuration_test_failed")
			return
		}
	}
	c.Redirect(http.StatusSeeOther, identity.ReturnTo)
}

func (h *AuthHandler) recordOIDCConfigTestFailure(c *gin.Context, actorUserID, code string) {
	if err := h.oidc.RecordAdminFailure(c.Request.Context(), actorUserID, "verify", oidcAuditRequestID(c), code); err != nil {
		log.Printf("[Auth] could not persist OIDC configuration-test failure audit")
	}
}

func callbackFailureAuditCode(err error) string {
	switch {
	case errors.Is(err, oidcauth.ErrInvalidIdentity):
		return "identity_validation_failed"
	case errors.Is(err, oidcauth.ErrProviderUnavailable):
		return "provider_unavailable"
	case errors.Is(err, oidcauth.ErrConfigurationChanged):
		return "oidc_configuration_changed"
	default:
		return "configuration_test_failed"
	}
}

type oidcCallbackFinalizationError struct {
	code string
}

func (e *oidcCallbackFinalizationError) Error() string {
	return "OIDC callback finalization failed: " + e.code
}

// finalizeOIDCCallback runs while the runtime holds the configuration read
// lease. Apply therefore cannot disable or replace the provider between token
// validation and these local identity/session side effects.
func (h *AuthHandler) finalizeOIDCCallback(c *gin.Context, state oidcruntime.State, identity oidcauth.AuthenticatedIdentity) string {
	if !state.Config.Enabled || !state.Ready {
		return "configuration_disabled"
	}
	oidcConfig := state.Config
	switch identity.Purpose {
	case oidcauth.PurposeLogin:
		user, _, err := store.ResolveOIDCLogin(c.Request.Context(), identity.VerifiedIdentity, store.OIDCProvisionPolicy{
			AutoProvision:  oidcConfig.AutoProvision,
			BootstrapAdmin: oidcConfig.IsBootstrapAdmin(identity.Subject),
		})
		if err != nil {
			if errors.Is(err, store.ErrOIDCIdentityNotLinked) || errors.Is(err, store.ErrOIDCBootstrapDenied) {
				return "account_not_authorized"
			}
			return "login_failed"
		}
		if err := h.issueSession(c, user.ID, model.SessionAuthMethodOIDC, oidcConfig.SessionAbsoluteTTL, oidcConfig.SecureCookies); err != nil {
			return "login_failed"
		}
	case oidcauth.PurposeLink:
		user, _, ok := currentTransactionSession(c, identity.SessionUserID, "")
		if !ok {
			return "session_mismatch"
		}
		if err := store.LinkOIDCIdentity(c.Request.Context(), user.ID, identity.VerifiedIdentity); err != nil {
			if errors.Is(err, store.ErrOIDCIdentityAlreadyLinked) || errors.Is(err, store.ErrOIDCProviderAlreadyLinked) {
				return "identity_already_linked"
			}
			return "link_failed"
		}
	case oidcauth.PurposeReauth:
		user, credential, ok := currentTransactionSession(c, identity.SessionUserID, "")
		if !ok {
			return "session_mismatch"
		}
		belongs, err := store.OIDCIdentityBelongsToUser(c.Request.Context(), user.ID, identity.Issuer, identity.Subject)
		if err != nil {
			return "reauth_failed"
		}
		if !belongs {
			return "identity_mismatch"
		}
		if err := store.MarkSessionAuthenticated(credential.ID, time.Now().UTC()); err != nil {
			return "reauth_failed"
		}
	default:
		return "login_failed"
	}
	return ""
}

// OIDCUnlink removes the configured provider only when another login method
// remains. Recent-authentication middleware protects this endpoint.
func (h *AuthHandler) OIDCUnlink(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Browser session required"})
		return
	}
	var oidcConfig config.OIDCConfig
	var passwordLoginPolicyDisabled bool
	err := h.oidc.WithStateLease(func(state oidcruntime.State) error {
		oidcConfig = state.Config
		passwordLoginPolicyDisabled = passwordLoginDisabledByPolicy(state)
		return store.UnlinkOIDCIdentity(c.Request.Context(), user.ID, oidcConfig.IssuerURL, !passwordLoginPolicyDisabled)
	})
	if err != nil {
		if errors.Is(err, store.ErrOIDCWouldLockOut) {
			message := "Set a local password before unlinking the last external login"
			if passwordLoginPolicyDisabled {
				message = "Enable password login before unlinking the last external login"
			}
			c.JSON(http.StatusConflict, gin.H{"error": message})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "OIDC identity is not linked"})
		return
	}
	c.Status(http.StatusNoContent)
}

func currentTransactionSession(c *gin.Context, expectedUserID, expectedSessionID string) (*model.AuthUser, *middleware.RequestCredential, bool) {
	user := middleware.GetCurrentUser(c)
	credential := middleware.GetCurrentCredential(c)
	return user, credential, user != nil && user.ID == expectedUserID && credential != nil && credential.Type == middleware.CredentialSession &&
		(expectedSessionID == "" || credential.ID == expectedSessionID)
}

func redirectOIDCFailure(c *gin.Context, returnTo, code string) {
	c.Redirect(http.StatusSeeOther, oidcErrorReturnTo(returnTo, code))
}

func oidcErrorReturnTo(returnTo, code string) string {
	target, err := url.Parse(returnTo)
	if err != nil || !strings.HasPrefix(target.Path, "/") || target.IsAbs() || target.Host != "" {
		return config.JoinBasePath("/")
	}
	query := target.Query()
	query.Set("oidc_error", code)
	target.RawQuery = query.Encode()
	return target.String()
}

func (h *AuthHandler) issueSession(c *gin.Context, userID, authMethod string, absoluteTTL time.Duration, secure bool) error {
	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(middleware.SessionMaxAge) * time.Second)
	maxAge := middleware.SessionMaxAge
	var absoluteExpiresAt *time.Time
	if absoluteTTL > 0 {
		absolute := now.Add(absoluteTTL)
		absoluteExpiresAt = &absolute
		if absolute.Before(expiresAt) {
			expiresAt = absolute
		}
		maxAge = int(time.Until(absolute).Seconds())
		if maxAge < 1 {
			maxAge = 1
		}
	}
	token := uuid.NewString()
	cookieSecure := secure
	if err := store.CreateSession(&model.UserSession{
		ID: token, UserID: userID, ExpiresAt: expiresAt, AuthMethod: authMethod,
		AuthenticatedAt: now, AbsoluteExpiresAt: absoluteExpiresAt, CookieSecure: &cookieSecure,
	}); err != nil {
		return err
	}
	middleware.SetSessionCookieWithOptions(c, token, maxAge, secure)
	return nil
}

func (h *AuthHandler) setOIDCTransactionCookie(c *gin.Context, value string, expiresAt time.Time, secure bool) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: OIDCTransactionCookie, Value: value, Path: config.JoinBasePath("/api/auth/oidc"),
		MaxAge: maxAge, Expires: expiresAt, HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *AuthHandler) clearOIDCTransactionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: OIDCTransactionCookie, Value: "", Path: config.JoinBasePath("/api/auth/oidc"),
		MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: h.oidc.State().Config.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}
