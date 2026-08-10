package middleware

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

const (
	SessionCookie              = "nowen_session"
	SessionMaxAge              = 30 * 24 * 60 * 60 // 30 days in seconds
	RecentAuthenticationWindow = 10 * time.Minute
)

// contextKey constants
const (
	ContextKeyUser       = "auth_user"
	ContextKeyCredential = "auth_credential"
	ContextKeySession    = "auth_session"
)

type CredentialType string

const (
	CredentialSession CredentialType = "session"
	CredentialAPIKey  CredentialType = "api_key"
)

type RequestCredential struct {
	Type CredentialType
	ID   string
}

// AuthRequired accepts either a valid browser session or API key.
func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := GetCurrentUser(c)
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		c.Set(ContextKeyUser, user)
		c.Next()
	}
}

// OPDSAuthRequired accepts browser sessions, Bearer API keys, and HTTP Basic
// credentials where the username is the API key owner's username and the
// password is the API key. Basic authentication is intentionally scoped to
// OPDS routes for compatibility with catalog clients.
func OPDSAuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if user := GetCurrentUser(c); user != nil {
			c.Set(ContextKeyUser, user)
			c.Next()
			return
		}

		username, token, ok := c.Request.BasicAuth()
		if ok && username != "" && token != "" {
			key, user, err := store.AuthenticateAPIKey(token)
			if err == nil && key != nil && user != nil && user.Username == username {
				authUser := authUserFromModel(user)
				setAuthenticatedUser(c, authUser, RequestCredential{Type: CredentialAPIKey, ID: key.ID})
				c.Next()
				return
			}
		}

		c.Header("WWW-Authenticate", `Basic realm="Nowen Reader OPDS", charset="UTF-8"`)
		c.Header("Cache-Control", "private, no-store")
		c.Header("Vary", "Authorization, Cookie")
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
	}
}

// SessionRequired only accepts a browser session. It protects credential
// management endpoints from being called with an API key.
func SessionRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := getCurrentSessionUser(c)
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Browser session required"})
			return
		}
		c.Next()
	}
}

// RequireRecentAuthentication protects credential-changing operations. It
// must run after SessionRequired and is satisfied by either a fresh password
// login or a completed OIDC reauthentication transaction.
func RequireRecentAuthentication(maxAge time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		session := GetCurrentSession(c)
		if session == nil {
			_ = getCurrentSessionUser(c)
			session = GetCurrentSession(c)
		}
		if !SessionAuthenticationIsRecent(session, maxAge, time.Now().UTC()) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Recent authentication required", "code": "reauth_required",
			})
			return
		}
		c.Next()
	}
}

// SessionAuthenticationIsRecent centralizes the recent-authentication clock
// policy so handlers and middleware cannot drift apart.
func SessionAuthenticationIsRecent(session *model.UserSession, maxAge time.Duration, now time.Time) bool {
	if session == nil || session.AuthenticatedAt.IsZero() {
		return false
	}
	age := now.Sub(session.AuthenticatedAt)
	return age >= -2*time.Minute && age <= maxAge
}

// AdminRequired is a middleware that requires an admin user.
func AdminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := GetCurrentUser(c)
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		if user.Role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Forbidden"})
			return
		}
		c.Set(ContextKeyUser, user)
		c.Next()
	}
}

// AIRequired is a middleware that requires the user to have AI access enabled.
// Admin users always have AI access. Non-admin users need explicit aiEnabled flag.
func AIRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := GetCurrentUser(c)
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		if user.Role != "admin" && !user.AiEnabled {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "AI access not enabled for your account"})
			return
		}
		c.Set(ContextKeyUser, user)
		c.Next()
	}
}

// RequireComicManagePermission is a middleware that requires the user to have manage access to the comic's library.
func RequireComicManagePermission() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := GetCurrentUser(c)
		if user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		comicID := c.Param("id")
		if comicID == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Comic ID required"})
			return
		}

		// Fast path for admin
		if user.Role == "admin" {
			c.Next()
			return
		}

		comic, err := store.GetComicByID(comicID)
		if err != nil || comic == nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Comic not found"})
			return
		}

		canManage, _ := store.UserCanManageLibrary(user.ID, comic.LibraryID)
		if !canManage {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Forbidden: No manage permission for this library"})
			return
		}

		c.Next()
	}
}

// GetCurrentUser resolves the explicit Bearer API key first, then falls back to
// the session cookie only when no Authorization header is present.
func GetCurrentUser(c *gin.Context) *model.AuthUser {
	if user := getContextUser(c); user != nil {
		return user
	}

	if authorization := c.GetHeader("Authorization"); authorization != "" {
		parts := strings.Fields(authorization)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return nil
		}

		key, user, err := store.AuthenticateAPIKey(parts[1])
		if err != nil || key == nil || user == nil {
			return nil
		}

		authUser := authUserFromModel(user)
		setAuthenticatedUser(c, authUser, RequestCredential{Type: CredentialAPIKey, ID: key.ID})
		return authUser
	}

	return getCurrentSessionUser(c)
}

// GetCurrentCredential returns the credential selected for this request.
func GetCurrentCredential(c *gin.Context) *RequestCredential {
	credential, exists := c.Get(ContextKeyCredential)
	if !exists {
		return nil
	}
	result, ok := credential.(RequestCredential)
	if !ok {
		return nil
	}
	return &result
}

// GetCurrentSession returns the local browser session selected for this
// request. It is nil for API-key credentials.
func GetCurrentSession(c *gin.Context) *model.UserSession {
	value, exists := c.Get(ContextKeySession)
	if !exists {
		return nil
	}
	session, _ := value.(*model.UserSession)
	return session
}

func getCurrentSessionUser(c *gin.Context) *model.AuthUser {
	if user := getContextUser(c); user != nil {
		credential := GetCurrentCredential(c)
		if credential != nil && credential.Type == CredentialSession {
			return user
		}
		return nil
	}

	// An explicit Authorization header always wins and cannot fall back to a
	// browser cookie, even when it is malformed or invalid.
	if c.GetHeader("Authorization") != "" {
		return nil
	}

	token, err := c.Cookie(SessionCookie)
	if err != nil || token == "" {
		return nil
	}

	session, user, err := store.GetSessionWithUser(token)
	if err != nil || session == nil || user == nil {
		return nil
	}

	now := time.Now().UTC()
	// Check both the sliding expiry and the non-renewable absolute boundary used
	// by federated sessions.
	if !session.ExpiresAt.After(now) || (session.AbsoluteExpiresAt != nil && !session.AbsoluteExpiresAt.After(now)) {
		// Clean up expired session
		_ = store.DeleteSession(token)
		return nil
	}

	// 自动续期：当 Session 剩余有效期不足 7 天时，自动延长到 30 天
	const renewThreshold = 7 * 24 * time.Hour
	if session.ExpiresAt.Sub(now) < renewThreshold {
		newExpiry := now.Add(time.Duration(SessionMaxAge) * time.Second)
		if session.AbsoluteExpiresAt != nil && session.AbsoluteExpiresAt.Before(newExpiry) {
			newExpiry = *session.AbsoluteExpiresAt
		}
		if newExpiry.After(session.ExpiresAt) && store.RenewSession(token, newExpiry) == nil {
			maxAge := int(newExpiry.Sub(now).Seconds())
			secure := session.AuthMethod == model.SessionAuthMethodOIDC && (IsRequestSecure(c) || !isLoopbackRequest(c))
			SetSessionCookieWithOptions(c, token, maxAge, secure)
		}
	}

	authUser := authUserFromModel(user)
	c.Set(ContextKeySession, session)
	setAuthenticatedUser(c, authUser, RequestCredential{Type: CredentialSession, ID: session.ID})
	return authUser
}

func getContextUser(c *gin.Context) *model.AuthUser {
	if value, exists := c.Get(ContextKeyUser); exists {
		user, _ := value.(*model.AuthUser)
		return user
	}
	return nil
}

func setAuthenticatedUser(c *gin.Context, user *model.AuthUser, credential RequestCredential) {
	c.Set(ContextKeyUser, user)
	c.Set(ContextKeyCredential, credential)
}

func authUserFromModel(user *model.User) *model.AuthUser {
	return &model.AuthUser{
		ID:          user.ID,
		Username:    user.Username,
		Nickname:    user.Nickname,
		Role:        user.Role,
		AiEnabled:   user.AiEnabled,
		HasPassword: user.Password != "",
	}
}

// IsRequestSecure determines if the request is over HTTPS.
// Checks X-Forwarded-Proto for reverse proxy scenarios (NAS/LAN).
func IsRequestSecure(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	forwarded := c.GetHeader("X-Forwarded-Proto")
	return strings.Contains(strings.ToLower(forwarded), "https")
}

func isLoopbackRequest(c *gin.Context) bool {
	host := c.Request.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}

// SetSessionCookie sets the session cookie on the response.
// 注意：不设置 Secure 标志，因为：
// 1. 本项目主要用于局域网/NAS 环境，很多用户通过 HTTP 访问
// 2. Flutter App (dio_cookie_manager) 在 HTTP 连接时不会发送 Secure Cookie
// 3. httpOnly=true 已经提供了足够的 XSS 防护
func SetSessionCookie(c *gin.Context, token string) {
	SetSessionCookieWithOptions(c, token, SessionMaxAge, false)
}

// SetSessionCookieWithOptions issues a local session cookie with an explicit
// lifetime and Secure policy. OIDC uses the externally configured HTTPS origin;
// legacy LAN/password sessions preserve their current HTTP compatibility.
func SetSessionCookieWithOptions(c *gin.Context, token string, maxAge int, secure bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	cookiePath := config.BasePath()
	c.SetCookie(SessionCookie, token, maxAge, cookiePath, "", secure, true)
}

// ClearSessionCookie removes the session cookie.
func ClearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	cookiePath := config.BasePath()
	c.SetCookie(SessionCookie, "", -1, cookiePath, "", false, true)
	if cookiePath != "/" {
		c.SetCookie(SessionCookie, "", -1, "/", "", false, true)
	}
}
