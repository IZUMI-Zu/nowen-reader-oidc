package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/auth/oidcruntime"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/middleware"
)

type OIDCAdminHandler struct {
	runtime *oidcruntime.Manager
	auth    *AuthHandler
}

type oidcAdminUpdateBody struct {
	ExpectedRevision            *int64                   `json:"expectedRevision"`
	Config                      *oidcruntime.AdminFields `json:"config"`
	ClientSecret                *string                  `json:"clientSecret"`
	ClearSecret                 bool                     `json:"clearClientSecret"`
	ConfirmOIDCOnlyUsers        bool                     `json:"confirmOIDCOnlyUsers"`
	ConfirmDisablePasswordLogin bool                     `json:"confirmDisablePasswordLogin"`
}

type oidcAdminProbeBody struct {
	Config       *oidcruntime.AdminFields `json:"config"`
	ClientSecret *string                  `json:"clientSecret"`
}

const maxOIDCAdminRequestBytes int64 = 64 << 10

func NewOIDCAdminHandler(runtime *oidcruntime.Manager, auth *AuthHandler) *OIDCAdminHandler {
	return &OIDCAdminHandler{runtime: runtime, auth: auth}
}

func (h *OIDCAdminHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC configuration is unavailable", "code": "configuration_unavailable"})
		return
	}
	result, err := h.runtime.AdminConfig(c.Request.Context())
	if err != nil {
		respondOIDCAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *OIDCAdminHandler) Update(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC configuration is unavailable", "code": "configuration_unavailable"})
		return
	}
	user := middleware.GetCurrentUser(c)
	if user == nil || user.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Administrator access required"})
		return
	}
	var body oidcAdminUpdateBody
	if err := decodeOIDCAdminJSON(c, &body); err != nil {
		h.recordFailure(c, user.ID, "update", "invalid_request")
		respondInvalidOIDCAdminRequest(c, err)
		return
	}
	if body.ExpectedRevision == nil || *body.ExpectedRevision < 0 || body.Config == nil {
		h.recordFailure(c, user.ID, "update", "invalid_request")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body", "code": "invalid_request"})
		return
	}
	result, err := h.runtime.Apply(c.Request.Context(), oidcruntime.UpdateRequest{
		ExpectedRevision:            *body.ExpectedRevision,
		ActorUserID:                 user.ID,
		RequestID:                   oidcAuditRequestID(c),
		Fields:                      *body.Config,
		ClientSecret:                body.ClientSecret,
		ClearSecret:                 body.ClearSecret,
		ConfirmOIDCOnlyUsers:        body.ConfirmOIDCOnlyUsers,
		ConfirmDisablePasswordLogin: body.ConfirmDisablePasswordLogin,
	})
	if err != nil {
		respondOIDCAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *OIDCAdminHandler) Probe(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC configuration is unavailable", "code": "configuration_unavailable"})
		return
	}
	var body oidcAdminProbeBody
	if err := decodeOIDCAdminJSON(c, &body); err != nil {
		h.recordFailure(c, currentOIDCAdminUserID(c), "probe", "invalid_request")
		respondInvalidOIDCAdminRequest(c, err)
		return
	}
	if body.Config == nil {
		h.recordFailure(c, currentOIDCAdminUserID(c), "probe", "invalid_request")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body", "code": "invalid_request"})
		return
	}
	result, err := h.runtime.Probe(c.Request.Context(), oidcruntime.ProbeRequest{
		Fields: *body.Config, ClientSecret: body.ClientSecret,
	})
	if err != nil {
		h.recordFailure(c, currentOIDCAdminUserID(c), "probe", adminOIDCErrorCode(err))
		if errors.Is(err, oidcruntime.ErrEnvironmentManaged) || errors.Is(err, oidcruntime.ErrConfigurationInvalid) {
			respondOIDCAdminError(c, err)
			return
		}
		log.Printf("[Auth] OIDC discovery probe failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "Could not validate the OIDC provider discovery document", "code": "probe_failed"})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *OIDCAdminHandler) BeginTestLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC configuration is unavailable", "code": "configuration_unavailable"})
		return
	}
	user := middleware.GetCurrentUser(c)
	if user == nil || user.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Administrator access required"})
		return
	}
	returnTo := config.JoinBasePath("/settings?tab=authentication")
	result, err := h.runtime.BeginConfigTest(c.Request.Context(), user.ID, returnTo)
	if err != nil {
		h.recordFailure(c, user.ID, "begin_verify", adminOIDCErrorCode(err))
		if errors.Is(err, oidcauth.ErrProviderUnavailable) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC provider is unavailable", "code": "provider_unavailable"})
			return
		}
		respondOIDCAdminError(c, err)
		return
	}
	h.auth.setOIDCTransactionCookie(c, result.BindingToken, result.ExpiresAt, result.CookieSecure)
	c.JSON(http.StatusOK, gin.H{"authorizationURL": result.URL})
}

func (h *OIDCAdminHandler) recordFailure(c *gin.Context, actorUserID, action, code string) {
	if h.runtime == nil {
		return
	}
	if err := h.runtime.RecordAdminFailure(c.Request.Context(), actorUserID, action, oidcAuditRequestID(c), code); err != nil {
		log.Printf("[Auth] could not persist OIDC admin failure audit")
	}
}

const oidcAuditRequestIDContextKey = "oidc-audit-request-id"

// oidcAuditRequestID deliberately does not trust the inbound X-Request-ID.
// OIDC administration handles secrets, so a client-controlled correlation value
// must not become an alternate path for writing secret material into the audit log.
func oidcAuditRequestID(c *gin.Context) string {
	if requestID, ok := c.Get(oidcAuditRequestIDContextKey); ok {
		return requestID.(string)
	}
	requestID := uuid.NewString()
	c.Set(oidcAuditRequestIDContextKey, requestID)
	c.Header("X-Request-ID", requestID)
	return requestID
}

func currentOIDCAdminUserID(c *gin.Context) string {
	if user := middleware.GetCurrentUser(c); user != nil {
		return user.ID
	}
	return ""
}

func adminOIDCErrorCode(err error) string {
	switch {
	case errors.Is(err, oidcruntime.ErrAdministratorRequired):
		return "administrator_required"
	case errors.Is(err, oidcruntime.ErrEnvironmentManaged):
		return "environment_managed"
	case errors.Is(err, oidcruntime.ErrConfigurationInvalid):
		return "configuration_invalid"
	case errors.Is(err, oidcauth.ErrProviderUnavailable):
		return "provider_unavailable"
	default:
		return "operation_failed"
	}
}

func decodeOIDCAdminJSON(c *gin.Context, destination any) error {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("content type must be application/json")
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxOIDCAdminRequestBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return err
	}
	return nil
}

func respondInvalidOIDCAdminRequest(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Request body is too large", "code": "invalid_request"})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body", "code": "invalid_request"})
}

func respondOIDCAdminError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, oidcruntime.ErrAdministratorRequired):
		c.JSON(http.StatusForbidden, gin.H{"error": "Administrator access required", "code": "administrator_required"})
	case errors.Is(err, oidcruntime.ErrEnvironmentManaged):
		c.JSON(http.StatusConflict, gin.H{"error": "OIDC configuration is managed by environment variables", "code": "environment_managed"})
	case errors.Is(err, oidcruntime.ErrConfigConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "OIDC configuration changed; reload before saving", "code": "config_revision_conflict"})
	case errors.Is(err, oidcruntime.ErrOIDCOnlyUsersConfirmation):
		c.JSON(http.StatusConflict, gin.H{"error": "Confirm the impact on OIDC-only users before disabling OIDC", "code": "oidc_only_users_confirmation_required"})
	case errors.Is(err, oidcruntime.ErrDisablePasswordConfirmation):
		c.JSON(http.StatusConflict, gin.H{"error": "Confirm that username and password login will be disabled", "code": "disable_password_confirmation_required"})
	case errors.Is(err, oidcruntime.ErrConfigurationUnverified):
		c.JSON(http.StatusConflict, gin.H{"error": "Complete a successful test login before enabling OIDC", "code": "test_login_required"})
	case errors.Is(err, oidcruntime.ErrAdminIdentityRequired):
		c.JSON(http.StatusConflict, gin.H{"error": "An administrator identity must be linked by a test login before enabling OIDC", "code": "admin_identity_required"})
	case errors.Is(err, oidcruntime.ErrBreakGlassPasswordRequired):
		c.JSON(http.StatusConflict, gin.H{"error": "Set a local administrator recovery password before disabling password login", "code": "recovery_password_required"})
	case errors.Is(err, oidcruntime.ErrConfigurationInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "OIDC configuration is invalid", "code": "configuration_invalid"})
	default:
		log.Printf("[Auth] OIDC admin operation failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "OIDC configuration operation failed", "code": "operation_failed"})
	}
}
