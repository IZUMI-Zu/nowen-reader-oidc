package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/middleware"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

const OIDCTransactionCookie = "nowen_oidc_tx"

func newAuthHandlerWithOIDC(cfg config.OIDCConfig, service oidcLoginService, err error) *AuthHandler {
	return &AuthHandler{oidcConfig: cfg, oidc: service, oidcErr: err}
}

func (h *AuthHandler) oidcStatus() gin.H {
	return gin.H{
		"enabled":     h.oidcConfig.Enabled && h.oidc != nil && h.oidcErr == nil,
		"displayName": h.oidcConfig.ProviderName,
	}
}

func (h *AuthHandler) oidcBootstrapReady() bool {
	return h.oidcConfig.Enabled && h.oidc != nil && h.oidcErr == nil &&
		h.oidcConfig.AutoProvision && len(h.oidcConfig.BootstrapAdminSubjects) > 0
}

// OIDCLogin starts a backend-managed Authorization Code + PKCE flow.
func (h *AuthHandler) OIDCLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.oidcConfig.Enabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "OIDC login is not enabled"})
		return
	}
	if h.oidcErr != nil || h.oidc == nil {
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
	h.setOIDCTransactionCookie(c, result.BindingToken, result.ExpiresAt)
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
	if !h.oidcConfig.Enabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "OIDC login is not enabled"})
		return
	}
	if h.oidcErr != nil || h.oidc == nil {
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
	h.setOIDCTransactionCookie(c, result.BindingToken, result.ExpiresAt)
	c.Redirect(http.StatusFound, result.URL)
}

// OIDCCallback completes the one-time authorization transaction and converts
// the verified external identity into a bounded local session.
func (h *AuthHandler) OIDCCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.oidcConfig.Enabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "OIDC login is not enabled"})
		return
	}
	if h.oidcErr != nil || h.oidc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC login is temporarily unavailable"})
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
	identity, err := h.oidc.Complete(c.Request.Context(), oidcauth.CallbackRequest{
		State: c.Query("state"), Code: c.Query("code"), BindingToken: binding,
	})
	if err != nil {
		switch {
		case errors.Is(err, oidcauth.ErrInvalidTransaction):
			c.JSON(http.StatusBadRequest, gin.H{"error": "OIDC login transaction is invalid or expired"})
		case errors.Is(err, oidcauth.ErrInvalidIdentity):
			redirectOIDCFailure(c, identity.ReturnTo, "identity_validation_failed")
		case errors.Is(err, oidcauth.ErrProviderUnavailable):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC provider is unavailable"})
		default:
			redirectOIDCFailure(c, identity.ReturnTo, "login_failed")
		}
		return
	}
	switch identity.Purpose {
	case oidcauth.PurposeLogin:
		user, _, err := store.ResolveOIDCLogin(c.Request.Context(), identity.VerifiedIdentity, store.OIDCProvisionPolicy{
			AutoProvision:  h.oidcConfig.AutoProvision,
			BootstrapAdmin: h.oidcConfig.IsBootstrapAdmin(identity.Subject),
		})
		if err != nil {
			if errors.Is(err, store.ErrOIDCIdentityNotLinked) || errors.Is(err, store.ErrOIDCBootstrapDenied) {
				redirectOIDCFailure(c, identity.ReturnTo, "account_not_authorized")
				return
			}
			redirectOIDCFailure(c, identity.ReturnTo, "login_failed")
			return
		}
		if err := h.issueSession(c, user.ID, model.SessionAuthMethodOIDC, h.oidcConfig.SessionAbsoluteTTL, h.oidcConfig.SecureCookies); err != nil {
			redirectOIDCFailure(c, identity.ReturnTo, "login_failed")
			return
		}
	case oidcauth.PurposeLink:
		user, _, ok := currentTransactionSession(c, identity.SessionUserID)
		if !ok {
			redirectOIDCFailure(c, identity.ReturnTo, "session_mismatch")
			return
		}
		if err := store.LinkOIDCIdentity(c.Request.Context(), user.ID, identity.VerifiedIdentity); err != nil {
			if errors.Is(err, store.ErrOIDCIdentityAlreadyLinked) || errors.Is(err, store.ErrOIDCProviderAlreadyLinked) {
				redirectOIDCFailure(c, identity.ReturnTo, "identity_already_linked")
				return
			}
			redirectOIDCFailure(c, identity.ReturnTo, "link_failed")
			return
		}
	case oidcauth.PurposeReauth:
		user, credential, ok := currentTransactionSession(c, identity.SessionUserID)
		if !ok {
			redirectOIDCFailure(c, identity.ReturnTo, "session_mismatch")
			return
		}
		belongs, err := store.OIDCIdentityBelongsToUser(c.Request.Context(), user.ID, identity.Issuer, identity.Subject)
		if err != nil {
			redirectOIDCFailure(c, identity.ReturnTo, "reauth_failed")
			return
		}
		if !belongs {
			redirectOIDCFailure(c, identity.ReturnTo, "identity_mismatch")
			return
		}
		if err := store.MarkSessionAuthenticated(credential.ID, time.Now().UTC()); err != nil {
			redirectOIDCFailure(c, identity.ReturnTo, "reauth_failed")
			return
		}
	default:
		redirectOIDCFailure(c, identity.ReturnTo, "login_failed")
		return
	}
	c.Redirect(http.StatusSeeOther, identity.ReturnTo)
}

// OIDCUnlink removes the configured provider only when another login method
// remains. Recent-authentication middleware protects this endpoint.
func (h *AuthHandler) OIDCUnlink(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Browser session required"})
		return
	}
	if err := store.UnlinkOIDCIdentity(c.Request.Context(), user.ID, h.oidcConfig.IssuerURL, !h.oidcConfig.DisablePasswordLogin); err != nil {
		if errors.Is(err, store.ErrOIDCWouldLockOut) {
			message := "Set a local password before unlinking the last external login"
			if h.oidcConfig.DisablePasswordLogin {
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

func currentTransactionSession(c *gin.Context, expectedUserID string) (*model.AuthUser, *middleware.RequestCredential, bool) {
	user := middleware.GetCurrentUser(c)
	credential := middleware.GetCurrentCredential(c)
	return user, credential, user != nil && user.ID == expectedUserID && credential != nil && credential.Type == middleware.CredentialSession
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
	if err := store.CreateSession(&model.UserSession{
		ID: token, UserID: userID, ExpiresAt: expiresAt, AuthMethod: authMethod,
		AuthenticatedAt: now, AbsoluteExpiresAt: absoluteExpiresAt,
	}); err != nil {
		return err
	}
	middleware.SetSessionCookieWithOptions(c, token, maxAge, secure)
	return nil
}

func (h *AuthHandler) setOIDCTransactionCookie(c *gin.Context, value string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: OIDCTransactionCookie, Value: value, Path: config.JoinBasePath("/api/auth/oidc"),
		MaxAge: maxAge, Expires: expiresAt, HttpOnly: true, Secure: h.oidcConfig.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *AuthHandler) clearOIDCTransactionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: OIDCTransactionCookie, Value: "", Path: config.JoinBasePath("/api/auth/oidc"),
		MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: h.oidcConfig.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}
